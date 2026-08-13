package server_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"openyou/daemon/internal/ai/llm"
	"openyou/daemon/internal/infra/bridge"
	"openyou/daemon/internal/infra/config"
	"openyou/daemon/internal/infra/crypto"
	"openyou/daemon/internal/infra/notify"
	"openyou/daemon/internal/infra/tool"
	"openyou/daemon/internal/server"
	"openyou/daemon/internal/store/consult"
	"openyou/daemon/internal/store/memory"
	"openyou/daemon/internal/store/task"
)

// fakeLLM is a controllable ChatStreamer for end-to-end tests. reply is a
// function of the full prompt/messages so tests can assert history replay.
type fakeLLM struct {
	replyFor func(joined string) string
}

func (f *fakeLLM) Chat(ctx context.Context, sys, user string) (string, error) {
	return f.replyFor(sys + "\n" + user), nil
}

func (f *fakeLLM) StreamChat(ctx context.Context, sys, user string, onDelta func(string)) (string, error) {
	reply := f.replyFor(sys + "\n" + user)
	for _, r := range reply {
		onDelta(string(r))
	}
	return reply, nil
}

func (f *fakeLLM) Stream(ctx context.Context, msgs []llm.Message, onDelta func(string)) (string, error) {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Role + ":" + m.Content + "\n")
	}
	reply := f.replyFor(b.String())
	// Stream it in small chunks to exercise the SSE + scrubber path.
	for _, r := range reply {
		onDelta(string(r))
	}
	return reply, nil
}

// e2eDeps builds a fully-wired router backed by in-memory SQLite + a fake LLM.
func e2eDeps(t *testing.T, llmc server.ChatStreamer) (http.Handler, server.Deps) {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	cfg, _ := config.NewStore(db)
	ss, _ := consult.NewStore(db)
	mem, _ := memory.NewStore(db)
	fb, _ := consult.NewStore(db)
	tk, _ := task.NewStore(db)

	deps := server.Deps{
		DB:       db,
		LLM:      llmc,
		Tools:    tool.NewRegistry(),
		Config:   cfg,
		Sessions: ss,
		Hub:      bridge.NewHub(),
		Memory:   mem,
		Feedback: fb,
		Tasks:    tk,
		Notify:   notify.NewQueue(16),
		DocsDir:  t.TempDir(),
	}
	return server.NewRouter(deps), deps
}

// sse posts to an SSE endpoint and returns the concatenated text of all
// text-delta / synthesis-delta events, plus any source-citations payload.
func sse(t *testing.T, h http.Handler, path, body string) (text string, citations []map[string]any, ctxLoaded bool) {
	t.Helper()
	w := doRequest(t, h, http.MethodPost, path, []byte(body))
	if w.Code != http.StatusOK {
		t.Fatalf("%s: status %d body=%s", path, w.Code, w.Body.String())
	}
	for _, line := range strings.Split(w.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var ev map[string]any
		if json.Unmarshal([]byte(payload), &ev) != nil {
			continue
		}
		switch ev["type"] {
		case "text-delta", "synthesis-delta":
			if s, ok := ev["text"].(string); ok {
				text += s
			}
		case "context-loaded":
			ctxLoaded = true
		case "source-citations":
			if arr, ok := ev["citations"].([]any); ok {
				for _, c := range arr {
					if m, ok := c.(map[string]any); ok {
						citations = append(citations, m)
					}
				}
			}
		}
	}
	return text, citations, ctxLoaded
}

// TestE2E_ContinuousConversation is the core VISION guarantee: openyou
// remembers earlier turns within a session.
func TestE2E_ContinuousConversation(t *testing.T) {
	llmc := &fakeLLM{replyFor: func(joined string) string {
		if strings.Contains(joined, "我叫张伟") {
			return "你叫张伟。"
		}
		return "你还没告诉我名字。"
	}}
	h, _ := e2eDeps(t, llmc)
	sid := "ses_e2e_name"

	// Turn 1: tell name.
	sse(t, h, "/api/chat", `{"message":"我叫张伟","session_id":"`+sid+`"}`)
	// Turn 2: ask name — THIS message alone has no name; only history does.
	got, _, _ := sse(t, h, "/api/chat", `{"message":"我是谁","session_id":"`+sid+`"}`)
	if !strings.Contains(got, "张伟") {
		t.Errorf("continuous memory failed: turn 2 should recall name, got %q", got)
	}
	// Isolation: a different session must NOT know the name.
	got2, _, _ := sse(t, h, "/api/chat", `{"message":"我是谁","session_id":"ses_other"}`)
	if strings.Contains(got2, "张伟") {
		t.Errorf("session isolation broken: other session knew the name: %q", got2)
	}
}

// TestE2E_ContextLoadedEmitted verifies the preload event fires.
func TestE2E_ContextLoadedEmitted(t *testing.T) {
	llmc := &fakeLLM{replyFor: func(string) string { return "ok" }}
	h, _ := e2eDeps(t, llmc)
	_, _, ctxLoaded := sse(t, h, "/api/chat", `{"message":"hi","session_id":"s1"}`)
	if !ctxLoaded {
		t.Errorf("expected a context-loaded SSE event")
	}
}

// TestE2E_ScrubberStripsThinkTags: model reasoning must never reach the user.
func TestE2E_ScrubberStripsInternalTags(t *testing.T) {
	llmc := &fakeLLM{replyFor: func(string) string {
		return "建议如下 <think>内部推理不该外泄</think> 直接私聊。"
	}}
	h, _ := e2eDeps(t, llmc)
	got, _, _ := sse(t, h, "/api/chat", `{"message":"怎么办","session_id":"s1"}`)
	if strings.Contains(got, "内部推理") || strings.Contains(got, "<think>") {
		t.Errorf("scrubber leaked internal tag: %q", got)
	}
	if !strings.Contains(got, "建议如下") || !strings.Contains(got, "直接私聊") {
		t.Errorf("scrubber dropped legitimate text: %q", got)
	}
}

// TestE2E_Citations verifies citation markers become structured source-citations.
func TestE2E_Citations(t *testing.T) {
	llmc := &fakeLLM{replyFor: func(string) string {
		return "私聊降级 [👤张建国档案]，用 [📋规则#2] 包装。"
	}}
	h, _ := e2eDeps(t, llmc)
	_, cites, _ := sse(t, h, "/api/chat", `{"message":"怎么应对","session_id":"s1"}`)
	if len(cites) < 2 {
		t.Fatalf("expected >=2 citations, got %d: %+v", len(cites), cites)
	}
	kinds := map[string]bool{}
	for _, c := range cites {
		kinds[c["from"].(string)] = true
	}
	if !kinds["person"] || !kinds["rule"] {
		t.Errorf("expected person+rule citations, got %+v", kinds)
	}
}

func newTestCipher() config.FieldCipher {
	key, _ := crypto.GenerateKey()
	c, _ := crypto.NewCipher(key)
	return c
}

func memEntry(content string, conf float64) memory.MemoryEntry {
	return memory.MemoryEntry{
		Type:       memory.MemoryPersonFact,
		Person:     "张三",
		Content:    content,
		Confidence: conf,
	}
}

func decodeInto(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode: %v", err)
	}
}
