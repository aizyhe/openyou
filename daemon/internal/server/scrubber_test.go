package server

import (
	"strings"
	"testing"
)

// feed pushes each delta through the scrubber and returns the concatenated
// safe output plus the final flush.
func feed(deltas []string) string {
	s := &streamScrubber{}
	var out strings.Builder
	for _, d := range deltas {
		out.WriteString(s.push(d))
	}
	out.WriteString(s.flush())
	return out.String()
}

func TestScrubberRemovesMemoryContext(t *testing.T) {
	in := "建议：" + memCtxOpen + "\n规则:不要在会上提案\n" + memCtxClose + " 你应该私聊。"
	got := feed([]string{in})
	if strings.Contains(got, "memory-context") || strings.Contains(got, "规则:不要在会上提案") {
		t.Errorf("memory-context block not stripped: %q", got)
	}
	if !strings.Contains(got, "建议：") || !strings.Contains(got, "你应该私聊") {
		t.Errorf("surrounding text lost: %q", got)
	}
}

func TestScrubberSplitAcrossDeltas(t *testing.T) {
	// The tag is split across many small deltas.
	full := "答案A " + memCtxOpen + "秘密" + memCtxClose + " 答案B"
	var deltas []string
	for _, r := range full {
		deltas = append(deltas, string(r))
	}
	got := feed(deltas)
	if strings.Contains(got, "memory-context") || strings.Contains(got, "秘密") {
		t.Errorf("split tag not stripped: %q", got)
	}
	if !strings.Contains(got, "答案A") || !strings.Contains(got, "答案B") {
		t.Errorf("surrounding text lost: %q", got)
	}
}

func TestScrubberRemovesThink(t *testing.T) {
	got := feed([]string{"before <think>internal reasoning</think> after"})
	if strings.Contains(got, "internal reasoning") || strings.Contains(got, "<think>") {
		t.Errorf("think block not stripped: %q", got)
	}
	if !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Errorf("surrounding text lost: %q", got)
	}
}

func TestScrubberPassesCleanText(t *testing.T) {
	clean := "这是一段没有任何标记的正常回复，包含 < 和 > 符号。"
	got := feed([]string{clean})
	if got != clean {
		t.Errorf("clean text altered:\n want %q\n got  %q", clean, got)
	}
}
