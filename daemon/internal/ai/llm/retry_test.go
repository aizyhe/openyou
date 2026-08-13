package llm

import (
	"context"
	"errors"
	"math/rand"
	"testing"
)

func TestClassifyError(t *testing.T) {
	cases := map[string]ErrorKind{
		"llm: status 429: slow down":         ErrRateLimit,
		"llm: status 500: oops":              ErrRetryable,
		"llm: status 401: invalid api key":   ErrAuth,
		"llm: status 413: context length":    ErrContextOverflow,
		"context length exceeded":            ErrContextOverflow,
		"llm: request failed: timeout":       ErrRetryable,
		"something totally unrecognized xyz": ErrUnknown,
	}
	for msg, want := range cases {
		if got := classifyError(errors.New(msg)); got != want {
			t.Errorf("classify(%q) = %v, want %v", msg, got, want)
		}
	}
	if classifyError(context.Canceled) != ErrUnknown {
		t.Errorf("context.Canceled should not be retryable (caller cancelled)")
	}
}

func TestWithRetrySucceedsAfterTransient(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	attempts := 0
	out, err := withRetry(context.Background(), rng, func() (string, error) {
		attempts++
		if attempts < 2 {
			return "", errors.New("llm: status 503: unavailable")
		}
		return "ok", nil
	})
	if err != nil || out != "ok" {
		t.Fatalf("want ok after retry, got %q err=%v", out, err)
	}
	if attempts != 2 {
		t.Errorf("want 2 attempts, got %d", attempts)
	}
}

func TestWithRetryDoesNotRetryFatal(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	attempts := 0
	_, err := withRetry(context.Background(), rng, func() (string, error) {
		attempts++
		return "", errors.New("llm: status 401: invalid api key")
	})
	if err == nil {
		t.Fatal("want error")
	}
	if attempts != 1 {
		t.Errorf("auth errors must not retry; got %d attempts", attempts)
	}
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	d1 := backoff(1, 1, rng)
	d5 := backoff(5, 1, rng)
	if d5 < d1 {
		t.Errorf("backoff should grow: d1=%v d5=%v", d1, d5)
	}
	if d5 > maxDelay+baseDelay {
		t.Errorf("backoff should cap near maxDelay, got %v", d5)
	}
}

func TestClassifyErrorNil(t *testing.T) {
	if classifyError(nil) != ErrUnknown {
		t.Errorf("nil error should classify as ErrUnknown")
	}
}

func TestBackoffCapsAtMaxDelay(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	// Large attempt with rateLimitMul forces d past maxDelay → cap branch.
	d := backoff(10, rateLimitMul, rng)
	if d > maxDelay+baseDelay {
		t.Errorf("backoff should cap at maxDelay(+jitter), got %v", d)
	}
	if d < maxDelay {
		t.Errorf("large attempt should reach maxDelay, got %v", d)
	}
}

func TestWithRetryRateLimitThenCancel(t *testing.T) {
	// A 429 (ErrRateLimit) sets mul=rateLimitMul; cancelling the context makes
	// the backoff select return ctx.Err() without a real sleep.
	ctx, cancel := context.WithCancel(context.Background())
	rng := rand.New(rand.NewSource(1))
	attempts := 0
	_, err := withRetry(ctx, rng, func() (string, error) {
		attempts++
		cancel() // cancel so the post-error backoff select exits immediately
		return "", errors.New("llm: status 429: too many requests")
	})
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if attempts != 1 {
		t.Errorf("expected 1 attempt before cancel, got %d", attempts)
	}
}

func TestWithRetryExhaustsAttempts(t *testing.T) {
	// All attempts fail with a retryable error → returns the last error after
	// maxRetries. Uses a rng but the final attempt returns without sleeping.
	rng := rand.New(rand.NewSource(1))
	attempts := 0
	_, err := withRetry(context.Background(), rng, func() (string, error) {
		attempts++
		return "", errors.New("llm: request failed: connection reset")
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if attempts != maxRetries {
		t.Errorf("expected %d attempts, got %d", maxRetries, attempts)
	}
}
