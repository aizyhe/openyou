package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// stubConfig is an in-memory ConfigProvider for driving Client.settings().
type stubConfig map[string]string

func (s stubConfig) Get(key string) (string, error) {
	if v, ok := s[key]; ok {
		return v, nil
	}
	return "", nil
}

// errConfig returns an error for a specific key to exercise the "_, _ :=" paths.
type errConfig struct{}

func (errConfig) Get(key string) (string, error) {
	return "", fmt.Errorf("boom")
}

// newChatServer returns an httptest.Server that serves a fixed non-streaming
// chat completion JSON body, and the client pointed at it.
func newChatServer(t *testing.T, reply string) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("missing/wrong auth header: %q", got)
		}
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": reply}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(stubConfig{"api_key": "test-key", "base_url": srv.URL, "model": "test-model"})
	return c, srv
}

func TestSettingsDefaults(t *testing.T) {
	// Nil config → all defaults.
	c := NewClient(nil)
	apiKey, baseURL, model, maxTokens, temp := c.settings()
	if apiKey != "" || baseURL != "https://api.deepseek.com/v1" || model != "deepseek-v4-pro" || maxTokens != 2048 || temp != 0.7 {
		t.Fatalf("unexpected defaults: %q %q %q %d %v", apiKey, baseURL, model, maxTokens, temp)
	}
}

func TestSettingsOverrides(t *testing.T) {
	c := NewClient(stubConfig{
		"api_key":     "k",
		"base_url":    "https://example.com/v1/", // trailing slash trimmed
		"model":       "m",
		"max_tokens":  "512",
		"temperature": "0.1",
	})
	apiKey, baseURL, model, maxTokens, temp := c.settings()
	if apiKey != "k" || baseURL != "https://example.com/v1" || model != "m" || maxTokens != 512 || temp != 0.1 {
		t.Fatalf("overrides not applied: %q %q %q %d %v", apiKey, baseURL, model, maxTokens, temp)
	}
}

func TestSettingsIgnoresInvalidNumbers(t *testing.T) {
	// Non-numeric / non-positive values fall back to defaults.
	c := NewClient(stubConfig{
		"api_key":     "k",
		"max_tokens":  "-5",
		"temperature": "not-a-float",
	})
	_, _, _, maxTokens, temp := c.settings()
	if maxTokens != 2048 || temp != 0.7 {
		t.Fatalf("invalid numbers should fall back: maxTokens=%d temp=%v", maxTokens, temp)
	}
}

func TestChatSuccess(t *testing.T) {
	c, _ := newChatServer(t, "hello world")
	out, err := c.Chat(context.Background(), "sys", "hi")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if out != "hello world" {
		t.Errorf("out = %q", out)
	}
}

func TestChatNoSystemPrompt(t *testing.T) {
	// Empty system prompt → only the user message is sent.
	var gotMsgs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotMsgs = len(req.Messages)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
		})
	}))
	t.Cleanup(srv.Close)
	c := NewClient(stubConfig{"api_key": "k", "base_url": srv.URL})
	if _, err := c.Chat(context.Background(), "", "hi"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if gotMsgs != 1 {
		t.Errorf("expected 1 message when system prompt empty, got %d", gotMsgs)
	}
}

func TestChatMessagesNoAPIKey(t *testing.T) {
	c := NewClient(stubConfig{}) // no api_key
	_, err := c.ChatMessages(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil || !strings.Contains(err.Error(), "api_key not configured") {
		t.Fatalf("expected api_key error, got %v", err)
	}
}

func TestChatNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 401 is non-retryable, so a single attempt.
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("invalid api key"))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(stubConfig{"api_key": "k", "base_url": srv.URL})
	_, err := c.Chat(context.Background(), "sys", "hi")
	if err == nil || !strings.Contains(err.Error(), "status 401") {
		t.Fatalf("expected status 401 error, got %v", err)
	}
}

func TestChatMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{not json"))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(stubConfig{"api_key": "k", "base_url": srv.URL})
	_, err := c.Chat(context.Background(), "sys", "hi")
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("expected decode error, got %v", err)
	}
}

func TestChatEmptyChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(stubConfig{"api_key": "k", "base_url": srv.URL})
	_, err := c.Chat(context.Background(), "sys", "hi")
	if err == nil || !strings.Contains(err.Error(), "empty response") {
		t.Fatalf("expected empty response error, got %v", err)
	}
}

func TestChatContextCancel(t *testing.T) {
	c := NewClient(stubConfig{"api_key": "k", "base_url": "http://127.0.0.1:0"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled → request fails immediately
	_, err := c.Chat(ctx, "sys", "hi")
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestChatRetryThen200(t *testing.T) {
	// Server returns 503 on first call, 200 after → withRetry makes 2 attempts.
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("unavailable"))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"recovered"}}]}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(stubConfig{"api_key": "k", "base_url": srv.URL})
	out, err := c.Chat(context.Background(), "sys", "hi")
	if err != nil {
		t.Fatalf("Chat after retry: %v", err)
	}
	if out != "recovered" {
		t.Errorf("out = %q", out)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected 2 attempts, got %d", got)
	}
}

// sseServer serves a fixed SSE body.
func newSSEServer(t *testing.T, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewClient(stubConfig{"api_key": "k", "base_url": srv.URL})
}

func TestStreamSuccess(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" world\"}}]}\n\n" +
		": comment line ignored\n\n" +
		"data: {\"choices\":[]}\n\n" + // empty choices → skipped
		"data: {bad json}\n\n" + // unparseable → skipped
		"data: [DONE]\n\n"
	c := newSSEServer(t, body)
	var deltas []string
	full, err := c.Stream(context.Background(), []Message{{Role: "user", Content: "hi"}}, func(d string) {
		deltas = append(deltas, d)
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if full != "Hello world" {
		t.Errorf("full = %q", full)
	}
	if strings.Join(deltas, "") != "Hello world" {
		t.Errorf("deltas = %v", deltas)
	}
}

func TestStreamNilCallback(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\ndata: [DONE]\n\n"
	c := newSSEServer(t, body)
	full, err := c.Stream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil || full != "x" {
		t.Fatalf("Stream nil cb: full=%q err=%v", full, err)
	}
}

func TestStreamNoAPIKey(t *testing.T) {
	c := NewClient(stubConfig{})
	_, err := c.Stream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "api_key not configured") {
		t.Fatalf("expected api_key error, got %v", err)
	}
}

func TestStreamNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("nope"))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(stubConfig{"api_key": "k", "base_url": srv.URL})
	_, err := c.Stream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "status 401") {
		t.Fatalf("expected status 401, got %v", err)
	}
}

func TestStreamRetryBeforeAnyDelta(t *testing.T) {
	// First attempt 503 (before any delta) → retried; second streams ok.
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("unavailable"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(stubConfig{"api_key": "k", "base_url": srv.URL})
	full, err := c.Stream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Stream retry: %v", err)
	}
	if full != "ok" {
		t.Errorf("full = %q", full)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected 2 attempts, got %d", got)
	}
}

func TestStreamContextCancelledBeforeStart(t *testing.T) {
	c := NewClient(stubConfig{"api_key": "k", "base_url": "http://127.0.0.1:0"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Stream(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestReadAll(t *testing.T) {
	r := strings.NewReader("hello there")
	s, err := readAll(r, 5)
	if err != nil {
		t.Fatalf("readAll: %v", err)
	}
	if s != "hello" {
		t.Errorf("readAll = %q, want %q", s, "hello")
	}
}

func TestErrConfigTolerated(t *testing.T) {
	// settings() ignores Get errors (uses "_, _ :=") → falls back to defaults.
	c := NewClient(errConfig{})
	apiKey, baseURL, _, _, _ := c.settings()
	if apiKey != "" || baseURL != "https://api.deepseek.com/v1" {
		t.Fatalf("errConfig should yield defaults, got apiKey=%q baseURL=%q", apiKey, baseURL)
	}
}

func TestStreamMidStreamContextCancel(t *testing.T) {
	// Cancel the context from within onDelta so the next scanner-loop select
	// hits ctx.Done() and returns ctx.Err() mid-stream.
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n\n" +
		"data: [DONE]\n\n"
	c := newSSEServer(t, body)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := c.Stream(ctx, []Message{{Role: "user", Content: "hi"}}, func(d string) {
		cancel() // cancel after first delta
	})
	if err == nil {
		t.Fatal("expected context cancellation error mid-stream")
	}
}

func TestStreamRateLimitRetryThenCancel(t *testing.T) {
	// Server returns 429 (ErrRateLimit → mul=rateLimitMul) then cancels the
	// context so the backoff select exits via ctx.Done() without a real sleep.
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("rate limit"))
		cancel()
	}))
	t.Cleanup(srv.Close)
	c := NewClient(stubConfig{"api_key": "k", "base_url": srv.URL})
	_, err := c.Stream(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error after rate-limit + cancel")
	}
}
