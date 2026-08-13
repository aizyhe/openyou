package llm

import (
	"context"
	"crypto/rand"
	"errors"
	"math"
	"math/big"
	mathrand "math/rand"
	"strings"
	"time"
)

type ErrorKind int

const (
	ErrUnknown         ErrorKind = iota
	ErrRetryable                 // transient: network, 5xx, timeout
	ErrRateLimit                 // 429: back off harder
	ErrContextOverflow           // prompt too long: caller should compact, not retry
	ErrAuth                      // 401/403/billing: fatal, do not retry
)

// classifyError inspects the error text since the client surfaces HTTP status
// in the message ("llm: status 429: ...").
func classifyError(err error) ErrorKind {
	if err == nil {
		return ErrUnknown
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ErrUnknown // caller cancelled or timed out — do not retry
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "status 429"), strings.Contains(s, "rate limit"), strings.Contains(s, "too many requests"):
		return ErrRateLimit
	case strings.Contains(s, "context length"), strings.Contains(s, "context_length"),
		strings.Contains(s, "maximum context"), strings.Contains(s, "too long"),
		strings.Contains(s, "status 413"):
		return ErrContextOverflow
	case strings.Contains(s, "status 401"), strings.Contains(s, "status 403"),
		strings.Contains(s, "invalid api key"), strings.Contains(s, "insufficient"),
		strings.Contains(s, "billing"), strings.Contains(s, "quota"):
		return ErrAuth
	// io.EOF signals normal HTTP stream completion and must not retry — retrying
	// would re-send completed requests and duplicate responses.
	case strings.Contains(s, "status 500"), strings.Contains(s, "status 502"),
		strings.Contains(s, "status 503"), strings.Contains(s, "status 504"),
		strings.Contains(s, "request failed"), strings.Contains(s, "timeout"),
		strings.Contains(s, "connection reset"):
		return ErrRetryable
	default:
		return ErrUnknown
	}
}

const (
	maxRetries   = 3
	baseDelay    = 500 * time.Millisecond
	maxDelay     = 8 * time.Second
	rateLimitMul = 2 // rate-limit errors wait longer
)

// cryptoJitter uses crypto/rand instead of math/rand to avoid thundering-herd
// when many goroutines call backoff at once with identical seeds.
func cryptoJitter(max time.Duration) time.Duration {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		// Extremely unlikely (entropy exhausted); fall back to a fixed fraction.
		return max / 2
	}
	return time.Duration(n.Int64())
}

// backoff returns a jittered exponential delay for attempt n (1-based).
// rng is accepted for interface compatibility (tests); jitter uses crypto/rand.
func backoff(n int, mul int, rng *mathrand.Rand) time.Duration {
	d := float64(baseDelay) * math.Pow(2, float64(n-1)) * float64(mul)
	if d > float64(maxDelay) {
		d = float64(maxDelay)
	}
	jitter := cryptoJitter(baseDelay)
	return time.Duration(d) + jitter
}

func (k ErrorKind) retryable() bool {
	return k == ErrRetryable || k == ErrRateLimit
}

// withRetry: overflow and auth errors return immediately — retrying won't help.
func withRetry(ctx context.Context, rng *mathrand.Rand, fn func() (string, error)) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		out, err := fn()
		if err == nil {
			return out, nil
		}
		lastErr = err
		kind := classifyError(err)
		if !kind.retryable() || attempt == maxRetries {
			return out, err
		}
		mul := 1
		if kind == ErrRateLimit {
			mul = rateLimitMul
		}
		select {
		case <-time.After(backoff(attempt, mul, rng)):
		case <-ctx.Done():
			return out, ctx.Err()
		}
	}
	return "", lastErr
}
