package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"openyou/daemon/internal/ai/llm"
	"openyou/daemon/internal/store/consult"
)

// chatErr is a ChatStreamer whose Chat always errors (for summarizeTurns).
type chatErr struct{}

func (chatErr) Chat(context.Context, string, string) (string, error) {
	return "", errors.New("boom")
}
func (chatErr) Stream(context.Context, []llm.Message, func(string)) (string, error) {
	return "", nil
}

// chatOK returns a fixed summary.
type chatOK struct{ out string }

func (c chatOK) Chat(context.Context, string, string) (string, error) { return c.out, nil }
func (c chatOK) Stream(context.Context, []llm.Message, func(string)) (string, error) {
	return "", nil
}

func TestRecentSplitSingleMessage(t *testing.T) {
	if got := recentSplit([]llm.Message{{Role: "user", Content: "x"}}); got != 0 {
		t.Errorf("single message split want 0, got %d", got)
	}
}

func TestRecentSplitWholeHistoryFits(t *testing.T) {
	msgs := []llm.Message{
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
		{Role: "user", Content: "c"},
	}
	if got := recentSplit(msgs); got != 0 {
		t.Errorf("small history should fit tail, split want 0, got %d", got)
	}
}

func TestRecentSplitSummarizesOldTurns(t *testing.T) {
	big := strings.Repeat("你", 5000) // ~5750 tokens each
	msgs := make([]llm.Message, 12)
	for i := range msgs {
		msgs[i] = llm.Message{Role: "user", Content: big}
	}
	split := recentSplit(msgs)
	if split <= 0 {
		t.Errorf("expected a positive split for oversized history, got %d", split)
	}
}

func TestCompactHistorySingleHugeTurn(t *testing.T) {
	// One turn over the threshold: can't summarize a single turn away → returned as-is.
	huge := strings.Repeat("你", 30000)
	prior := []*consult.Message{{Role: "user", Content: huge}}
	got := compactHistory(context.Background(), chatOK{out: "s"}, prior)
	if len(got) != 1 {
		t.Errorf("single huge turn should pass through, got %d", len(got))
	}
}

func TestCompactHistorySummarizes(t *testing.T) {
	big := strings.Repeat("你", 3000)
	prior := make([]*consult.Message, 20)
	for i := range prior {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		prior[i] = &consult.Message{Role: role, Content: big}
	}
	got := compactHistory(context.Background(), chatOK{out: "摘要内容"}, prior)
	if len(got) == 0 || got[0].Role != "system" {
		t.Fatalf("expected leading system summary, got %+v", got)
	}
	if !strings.Contains(got[0].Content, "摘要内容") {
		t.Errorf("summary not embedded: %q", got[0].Content)
	}
}

func TestSummarizeTurnsError(t *testing.T) {
	old := []llm.Message{{Role: "user", Content: "x"}}
	if got := summarizeTurns(context.Background(), chatErr{}, old); got != "" {
		t.Errorf("summarizeTurns should return empty on error, got %q", got)
	}
	// nil client / empty input.
	if got := summarizeTurns(context.Background(), nil, old); got != "" {
		t.Errorf("nil client should return empty, got %q", got)
	}
	if got := summarizeTurns(context.Background(), chatOK{out: "x"}, nil); got != "" {
		t.Errorf("empty old should return empty, got %q", got)
	}
}

func TestScrubFinalDropsUnclosedTag(t *testing.T) {
	got := scrubFinal("visible text <think>internal reasoning never closed")
	if strings.Contains(got, "internal") || strings.Contains(got, "<think>") {
		t.Errorf("unclosed tag not dropped: %q", got)
	}
	if !strings.Contains(got, "visible text") {
		t.Errorf("legit text dropped: %q", got)
	}
}

func TestToStrBool(t *testing.T) {
	if toStr(true) != "true" || toStr(false) != "false" {
		t.Error("bool toStr wrong")
	}
	if toStr(nil) != "" {
		t.Error("nil toStr should be empty")
	}
	if toStr(map[string]int{"a": 1}) == "" {
		t.Error("default toStr should json-marshal")
	}
	if toStr(1.5) != "1.5" {
		t.Errorf("frac float wrong: %q", toStr(1.5))
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 60); got != "short" {
		t.Errorf("short should be unchanged, got %q", got)
	}
	long := strings.Repeat("字", 100)
	got := truncate(long, 10)
	if !strings.HasSuffix(got, "…") || len([]rune(got)) != 11 {
		t.Errorf("truncate wrong: %q (%d runes)", got, len([]rune(got)))
	}
}

func TestParseCurationsFenceStripping(t *testing.T) {
	valid := map[string]bool{"a": true, "b": true}
	// Leading code fence directly (HasPrefix "```").
	resp := "```json\n[{\"action\":\"merge\",\"skills\":[\"a\",\"b\"],\"reason\":\"x\"}]\n```"
	got := parseCurations(resp, valid)
	if len(got) != 1 || got[0].Action != "merge" {
		t.Errorf("fence-stripped parse wrong: %+v", got)
	}
}

func TestParseCitationsUnknownEmojiSkipped(t *testing.T) {
	// A well-formed citation plus a bracketed non-citation are handled; only the
	// valid marker is returned.
	got := parseCitations("[👤张三档案] 普通 [不是引用]")
	if len(got) != 1 || got[0].From != "person" {
		t.Errorf("expected 1 person citation, got %+v", got)
	}
}
