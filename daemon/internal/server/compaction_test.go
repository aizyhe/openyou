package server

import (
	"context"
	"strings"
	"testing"

	"openyou/daemon/internal/ai/llm"
	"openyou/daemon/internal/store/consult"
)

func mkMsgs(n int, filler string) []*consult.Message {
	out := make([]*consult.Message, 0, n)
	for i := 0; i < n; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		out = append(out, &consult.Message{Role: role, Content: filler})
	}
	return out
}

func TestCompactHistorySmallPassthrough(t *testing.T) {
	prior := mkMsgs(4, "hi")
	got := compactHistory(context.Background(), nil, prior)
	if len(got) != 4 {
		t.Fatalf("small history should pass through unchanged, got %d", len(got))
	}
}

func TestCompactHistoryDropsSystemRows(t *testing.T) {
	prior := []*consult.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}
	got := compactHistory(context.Background(), nil, prior)
	for _, m := range got {
		if m.Role == "system" {
			t.Errorf("stored system rows must be dropped")
		}
	}
	if len(got) != 2 {
		t.Fatalf("want 2 non-system msgs, got %d", len(got))
	}
}

// With a nil LLM and an over-threshold backlog, compaction can't summarize, so
// it hard-trims to the recent turns — the crash-prevention fallback.
func TestCompactHistoryFallbackTrimsWhenOverThreshold(t *testing.T) {
	big := strings.Repeat("你", 4000) // ~2000 tokens each
	prior := mkMsgs(20, big)         // ~40000 tokens, well over threshold
	got := compactHistory(context.Background(), nil, prior)
	if len(got) >= 20 || len(got) == 0 {
		t.Fatalf("expected a trimmed tail (fewer than 20), got %d", len(got))
	}
	tail := 0
	for _, m := range got {
		tail += estimateTokens(m.Content)
	}
	if tail > recentMaxTokens+estimateTokens(strings.Repeat("你", 4000)) {
		t.Errorf("nil-LLM fallback tail %d exceeds ceiling %d", tail, recentMaxTokens)
	}
}

// Guards the compaction-overflow bug: very large turns below the min-turns
// floor must NOT all stay verbatim — the token ceiling is a hard bound.
func TestRecentSplitHardCeiling(t *testing.T) {
	big := strings.Repeat("你", 4000) // ~2000+ est tokens each
	msgs := make([]llm.Message, 8)   // 8 huge turns, ~18k+ tokens total
	for i := range msgs {
		msgs[i] = llm.Message{Role: "user", Content: big}
	}
	split := recentSplit(msgs)
	if split == 0 {
		t.Fatal("8 huge turns exceed the ceiling but recentSplit kept all — overflow bug")
	}
	tail := 0
	for _, m := range msgs[split:] {
		tail += estimateTokens(m.Content)
	}
	// Tail must not exceed the ceiling by more than one (unavoidable) turn.
	if tail > recentMaxTokens+estimateTokens(big) {
		t.Errorf("retained tail %d tokens exceeds ceiling %d", tail, recentMaxTokens)
	}
}

func TestEstimateTokens(t *testing.T) {
	if estimateTokens("") < 1 {
		t.Errorf("empty should still be >=1")
	}
	long := strings.Repeat("你", 100)
	if estimateTokens(long) < 40 {
		t.Errorf("100 CJK chars should estimate ~50 tokens, got %d", estimateTokens(long))
	}
}
