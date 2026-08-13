package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// fakeMasker maps "Zhang Wei"→"Engineer A" and back.
type fakeMasker struct{}

func (fakeMasker) Mask(s string) string   { return strings.ReplaceAll(s, "张伟", "Engineer A") }
func (fakeMasker) Unmask(s string) string { return strings.ReplaceAll(s, "Engineer A", "张伟") }

func TestMaskingClientNilMaskerPassthrough(t *testing.T) {
	m := NewMaskingClient(nil, nil)
	if got := m.mask("张伟"); got != "张伟" {
		t.Errorf("nil masker should passthrough, got %q", got)
	}
	if got := m.unmask("张伟"); got != "张伟" {
		t.Errorf("nil masker should passthrough, got %q", got)
	}
}

func TestMaskingClientMaskUnmask(t *testing.T) {
	m := NewMaskingClient(nil, fakeMasker{})
	if got := m.mask("你好张伟"); got != "你好Engineer A" {
		t.Errorf("mask: got %q", got)
	}
	if got := m.unmask("你好Engineer A"); got != "你好张伟" {
		t.Errorf("unmask: got %q", got)
	}
}

// The Stream/Chat integration paths hit a real HTTP endpoint, so they are
// covered by the server package's end-to-end tests. Here we only assert the
// mask/unmask plumbing, which is the privacy-critical part.
var _ = context.Background

// streamDeltaFunc reuses MaskingClient.Stream's unmask logic via a fake inner,
// exercising the flush algorithm without a real HTTP client or a full fake
// *Client (unexported fields) — tested at the algorithm level below.

// windowMasker maps a real name to a codename and reports a mask window.
type windowMasker struct {
	real, pseudo string
	window       int
}

func (w windowMasker) Mask(s string) string   { return replaceAll(s, w.real, w.pseudo) }
func (w windowMasker) Unmask(s string) string { return replaceAll(s, w.pseudo, w.real) }
func (w windowMasker) MaskWindow() int        { return w.window }

func replaceAll(s, old, new string) string { return strings.ReplaceAll(s, old, new) }

// simulateStream feeds deltas through the same hold-window unmask algorithm
// MaskingClient.Stream uses, returning what the user would see and the final.
func simulateStream(m *MaskingClient, deltas []string) (emitted, final string) {
	hold := holdBytesFor(m.masker)
	var rawFull string
	var emittedLen int
	var out strings.Builder
	flush := func(fin bool) {
		u := m.unmask(rawFull)
		safe := len(u)
		if !fin {
			safe -= hold
		}
		if safe <= emittedLen {
			return
		}
		for safe > emittedLen && safe < len(u) && !utf8Start(u[safe]) {
			safe--
		}
		if safe > emittedLen {
			out.WriteString(u[emittedLen:safe])
			emittedLen = safe
		}
	}
	for _, d := range deltas {
		rawFull += d
		flush(false)
	}
	flush(true)
	return out.String(), m.unmask(rawFull)
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func TestStreamNeverLeaksCodenameAcrossDeltas(t *testing.T) {
	m := NewMaskingClient(nil, windowMasker{real: "张伟", pseudo: "Engineer A", window: 16})
	// Codename split across deltas, then more text, including multibyte.
	deltas := []string{"Engineer", " A", " is great 你好"}
	emitted, final := simulateStream(m, deltas)
	if final != "张伟 is great 你好" {
		t.Fatalf("final: got %q", final)
	}
	if emitted != final {
		t.Errorf("emitted stream must equal final; emitted=%q final=%q", emitted, final)
	}
	if strings.Contains(emitted, "Engineer") {
		t.Errorf("codename leaked to user: %q", emitted)
	}
	if !utf8.ValidString(emitted) {
		t.Errorf("emitted contains invalid UTF-8: %q", emitted)
	}
}

func TestStreamValidUTF8OnRuneSplit(t *testing.T) {
	m := NewMaskingClient(nil, windowMasker{real: "李雷", pseudo: "Engineer B", window: 16})
	deltas := []string{"你", "好", "Engineer", " B", "再见"}
	emitted, final := simulateStream(m, deltas)
	if !utf8.ValidString(emitted) {
		t.Errorf("invalid UTF-8 in emitted: %q", emitted)
	}
	if emitted != final || strings.Contains(emitted, "Engineer") {
		t.Errorf("emitted=%q final=%q", emitted, final)
	}
}

// echoNameServer replies with the given fixed reply for non-streaming, and a
// fixed SSE stream for streaming requests. It also records the last request
// body so tests can assert outbound masking.
type echoNameServer struct {
	reply     string
	streamSSE string
	lastBody  chatRequest
}

func (s *echoNameServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		s.lastBody = req
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(s.streamSSE))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": s.reply}}},
		})
	}
}

func TestMaskingClientChatMasksAndUnmasks(t *testing.T) {
	// Reply contains a codename; unmask restores the real name.
	srv := &echoNameServer{reply: "I talked to Engineer A today"}
	ts := httptest.NewServer(srv.handler(t))
	t.Cleanup(ts.Close)
	inner := NewClient(stubConfig{"api_key": "k", "base_url": ts.URL})
	mc := NewMaskingClient(inner, fakeMasker{})

	out, err := mc.Chat(context.Background(), "sys 张伟", "hi 张伟")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if out != "I talked to 张伟 today" {
		t.Errorf("unmasked reply = %q", out)
	}
	// The outbound user message must have been masked.
	if !strings.Contains(srv.lastBody.Messages[len(srv.lastBody.Messages)-1].Content, "Engineer A") {
		t.Errorf("outbound message not masked: %+v", srv.lastBody.Messages)
	}
}

func TestMaskingClientChatError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	inner := NewClient(stubConfig{"api_key": "k", "base_url": srv.URL})
	mc := NewMaskingClient(inner, fakeMasker{})
	if _, err := mc.Chat(context.Background(), "sys", "hi"); err == nil {
		t.Fatal("expected error propagated from inner Chat")
	}
}

func TestMaskingClientChatMessages(t *testing.T) {
	srv := &echoNameServer{reply: "reply about Engineer A"}
	ts := httptest.NewServer(srv.handler(t))
	t.Cleanup(ts.Close)
	inner := NewClient(stubConfig{"api_key": "k", "base_url": ts.URL})
	mc := NewMaskingClient(inner, fakeMasker{})

	out, err := mc.ChatMessages(context.Background(), []Message{
		{Role: "system", Content: "you know 张伟"},
		{Role: "user", Content: "tell me about 张伟"},
	})
	if err != nil {
		t.Fatalf("ChatMessages: %v", err)
	}
	if out != "reply about 张伟" {
		t.Errorf("unmasked = %q", out)
	}
	for _, m := range srv.lastBody.Messages {
		if strings.Contains(m.Content, "张伟") {
			t.Errorf("real name leaked outbound: %q", m.Content)
		}
	}
}

func TestMaskingClientChatMessagesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	inner := NewClient(stubConfig{"api_key": "k", "base_url": srv.URL})
	mc := NewMaskingClient(inner, fakeMasker{})
	if _, err := mc.ChatMessages(context.Background(), []Message{{Role: "user", Content: "hi"}}); err == nil {
		t.Fatal("expected error")
	}
}

func TestMaskingClientStreamUnmasks(t *testing.T) {
	// Codename split across SSE deltas: "Engineer" + " A". The hold window keeps
	// the raw codename from leaking; final output has the real name.
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"Hi \"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"Engineer\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" A\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" done\"}}]}\n\n" +
		"data: [DONE]\n\n"
	srv := &echoNameServer{streamSSE: sse}
	ts := httptest.NewServer(srv.handler(t))
	t.Cleanup(ts.Close)
	inner := NewClient(stubConfig{"api_key": "k", "base_url": ts.URL})
	mc := NewMaskingClient(inner, windowMasker{real: "张伟", pseudo: "Engineer A", window: 16})

	var sb strings.Builder
	final, err := mc.Stream(context.Background(), []Message{{Role: "user", Content: "hi 张伟"}}, func(d string) {
		sb.WriteString(d)
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if final != "Hi 张伟 done" {
		t.Errorf("final = %q", final)
	}
	if strings.Contains(sb.String(), "Engineer") {
		t.Errorf("codename leaked to user: %q", sb.String())
	}
	if sb.String() != final {
		t.Errorf("emitted %q != final %q", sb.String(), final)
	}
	// Outbound message masked.
	if !strings.Contains(srv.lastBody.Messages[0].Content, "Engineer A") {
		t.Errorf("outbound not masked: %q", srv.lastBody.Messages[0].Content)
	}
}

func TestMaskingClientStreamError(t *testing.T) {
	// Inner stream fails (401) → error propagated, partial (empty) unmasked returned.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	inner := NewClient(stubConfig{"api_key": "k", "base_url": srv.URL})
	mc := NewMaskingClient(inner, fakeMasker{})
	final, err := mc.Stream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if final != "" {
		t.Errorf("final should be empty on early error, got %q", final)
	}
}

func TestMaskingClientNilMaskerStream(t *testing.T) {
	// Nil masker → holdBytes()==0, pass-through streaming.
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"plain text\"}}]}\n\ndata: [DONE]\n\n"
	srv := &echoNameServer{streamSSE: sse}
	ts := httptest.NewServer(srv.handler(t))
	t.Cleanup(ts.Close)
	inner := NewClient(stubConfig{"api_key": "k", "base_url": ts.URL})
	mc := NewMaskingClient(inner, nil)
	if got := holdBytesFor(mc.masker); got != 0 {
		t.Errorf("nil masker holdBytes = %d, want 0", got)
	}
	final, err := mc.Stream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil || final != "plain text" {
		t.Fatalf("nil-masker stream: final=%q err=%v", final, err)
	}
}

func TestHoldBytesDefaultAndWindow(t *testing.T) {
	// Masker without MaskWindow → default 64.
	mc := NewMaskingClient(nil, fakeMasker{})
	if got := holdBytesFor(mc.masker); got != 64 {
		t.Errorf("default holdBytes = %d, want 64", got)
	}
	// Masker with a positive window → that window.
	mc2 := NewMaskingClient(nil, windowMasker{real: "x", pseudo: "y", window: 32})
	if got := holdBytesFor(mc2.masker); got != 32 {
		t.Errorf("window holdBytes = %d, want 32", got)
	}
	// Masker advertising window 0 → falls back to default 64.
	mc3 := NewMaskingClient(nil, windowMasker{real: "x", pseudo: "y", window: 0})
	if got := holdBytesFor(mc3.masker); got != 64 {
		t.Errorf("zero-window holdBytes = %d, want 64 default", got)
	}
}
