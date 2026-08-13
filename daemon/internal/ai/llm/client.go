// Package llm provides an OpenAI-compatible client (DeepSeek endpoints) with
// blocking Chat and SSE Stream, reading config live so settings apply immediately.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type ConfigProvider interface {
	Get(key string) (string, error)
}

type Client struct {
	cfg  ConfigProvider
	http *http.Client
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func NewClient(cfg ConfigProvider) *Client {
	return &Client{
		cfg: cfg,
		// No global http.Client timeout — it would kill streams mid-reply;
		// Transport bounds connect/headers, the body read follows the caller's ctx.
		http: &http.Client{
			Transport: &http.Transport{
				DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
	}
}

func (c *Client) settings() (apiKey, baseURL, model string, maxTokens int, temperature float64) {
	baseURL = "https://api.deepseek.com/v1"
	model = "deepseek-v4-pro"
	maxTokens = 2048
	temperature = 0.7
	if c.cfg == nil {
		return
	}
	if v, _ := c.cfg.Get("api_key"); v != "" {
		apiKey = v
	}
	if v, _ := c.cfg.Get("base_url"); v != "" {
		baseURL = strings.TrimRight(v, "/")
	}
	if v, _ := c.cfg.Get("model"); v != "" {
		model = v
	}
	if v, _ := c.cfg.Get("max_tokens"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxTokens = n
		}
	}
	if v, _ := c.cfg.Get("temperature"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			temperature = f
		}
	}
	return
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature"`
	Stream      bool      `json:"stream"`
}

func (c *Client) Chat(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	msgs := make([]Message, 0, 2)
	if systemPrompt != "" {
		msgs = append(msgs, Message{Role: "system", Content: systemPrompt})
	}
	msgs = append(msgs, Message{Role: "user", Content: userMessage})
	return c.ChatMessages(ctx, msgs)
}

func (c *Client) StreamChat(ctx context.Context, systemPrompt, userMessage string, onDelta func(string)) (string, error) {
	msgs := make([]Message, 0, 2)
	if systemPrompt != "" {
		msgs = append(msgs, Message{Role: "system", Content: systemPrompt})
	}
	msgs = append(msgs, Message{Role: "user", Content: userMessage})
	return c.Stream(ctx, msgs, onDelta)
}

func (c *Client) ChatMessages(ctx context.Context, messages []Message) (string, error) {
	// Go 1.22+ auto-seeds the global rand; backoff uses crypto/rand for jitter.
	return withRetry(ctx, nil, func() (string, error) {
		return c.chatMessagesOnce(ctx, messages)
	})
}

func (c *Client) chatMessagesOnce(ctx context.Context, messages []Message) (string, error) {
	apiKey, baseURL, model, maxTokens, temperature := c.settings()
	if apiKey == "" {
		return "", fmt.Errorf("llm: api_key not configured")
	}

	reqBody := chatRequest{
		Model:       model,
		Messages:    messages,
		MaxTokens:   maxTokens,
		Temperature: temperature,
		Stream:      false,
	}
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("llm: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return "", fmt.Errorf("llm: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := readAll(resp.Body, 2048)
		return "", fmt.Errorf("llm: status %d: %s", resp.StatusCode, body)
	}

	var out struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("llm: decode response: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("llm: empty response")
	}
	return out.Choices[0].Message.Content, nil
}

// Stream sends the message list, invoking onDelta per chunk, and returns the
// accumulated text. Retries only before any delta is emitted — after that,
// mid-stream errors pass through (the chat handler re-prompts "continue").
func (c *Client) Stream(ctx context.Context, messages []Message, onDelta func(string)) (string, error) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	var full string
	var emittedAny bool
	wrapped := func(delta string) {
		emittedAny = true
		if onDelta != nil {
			onDelta(delta)
		}
	}
	for attempt := 1; attempt <= maxRetries; attempt++ {
		emittedAny = false
		var err error
		full, err = c.streamOnce(ctx, messages, wrapped)
		if err == nil {
			return full, nil
		}
		kind := classifyError(err)
		// Only safe to retry when nothing was emitted yet.
		if emittedAny || !kind.retryable() || attempt == maxRetries {
			return full, err
		}
		mul := 1
		if kind == ErrRateLimit {
			mul = rateLimitMul
		}
		select {
		case <-time.After(backoff(attempt, mul, rng)):
		case <-ctx.Done():
			return full, ctx.Err()
		}
	}
	return full, nil
}

func (c *Client) streamOnce(ctx context.Context, messages []Message, onDelta func(string)) (string, error) {
	apiKey, baseURL, model, maxTokens, temperature := c.settings()
	if apiKey == "" {
		return "", fmt.Errorf("llm: api_key not configured")
	}

	reqBody := chatRequest{
		Model:       model,
		Messages:    messages,
		MaxTokens:   maxTokens,
		Temperature: temperature,
		Stream:      true,
	}
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("llm: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return "", fmt.Errorf("llm: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := readAll(resp.Body, 2048)
		return "", fmt.Errorf("llm: status %d: %s", resp.StatusCode, body)
	}

	var full strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta.Content
		if delta != "" {
			full.WriteString(delta)
			if onDelta != nil {
				onDelta(delta)
			}
		}
		select {
		case <-ctx.Done():
			return full.String(), ctx.Err()
		default:
		}
	}
	if err := scanner.Err(); err != nil {
		return full.String(), fmt.Errorf("llm: stream read: %w", err)
	}
	return full.String(), nil
}

func readAll(r interface{ Read([]byte) (int, error) }, max int) (string, error) {
	buf := make([]byte, max)
	n, _ := r.Read(buf)
	return string(buf[:n]), nil
}
