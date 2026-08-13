package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// captureSSE runs fn with a fresh sseWriter backed by a recorder and returns
// the raw SSE body.
func captureSSE(t *testing.T, fn func(*sseWriter)) string {
	t.Helper()
	w := httptest.NewRecorder()
	sse, ok := newSSE(w)
	if !ok {
		t.Fatal("recorder should support flushing")
	}
	fn(sse)
	return w.Body.String()
}

func TestEmitCitationsWithMarkers(t *testing.T) {
	body := captureSSE(t, func(s *sseWriter) {
		emitCitations(s, "建议私聊 [👤张三档案] 并引用 [📋规则#1]。")
	})
	if !strings.Contains(body, "source-citations") ||
		!strings.Contains(body, "\"person\"") || !strings.Contains(body, "\"rule\"") {
		t.Errorf("expected person+rule citations, got: %s", body)
	}
	if strings.Contains(body, "unverified") {
		t.Errorf("cited reply must not be flagged unverified: %s", body)
	}
}

func TestEmitCitationsUnverifiedForUngroundedReply(t *testing.T) {
	// A substantive reply with NO markers → flagged unverified (VISION gap fix).
	long := strings.Repeat("这是一段没有任何来源标注的较长建议内容。", 3)
	body := captureSSE(t, func(s *sseWriter) { emitCitations(s, long) })
	if !strings.Contains(body, "unverified") {
		t.Errorf("ungrounded substantive reply should be flagged unverified, got: %s", body)
	}
}

func TestEmitCitationsSkipsTrivialReply(t *testing.T) {
	// A short ack should NOT be flagged (no noise).
	body := captureSSE(t, func(s *sseWriter) { emitCitations(s, "好的。") })
	if strings.Contains(body, "source-citations") {
		t.Errorf("trivial reply should emit no citation event, got: %s", body)
	}
}
