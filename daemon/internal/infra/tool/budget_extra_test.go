package tool

import "testing"

func TestNewIterationBudget_Floor(t *testing.T) {
	// n < 1 is clamped to 1.
	b := NewIterationBudget(0)
	if b.Remaining() != 1 {
		t.Errorf("Remaining = %d, want 1 (clamped)", b.Remaining())
	}
	b2 := NewIterationBudget(-5)
	if b2.Remaining() != 1 {
		t.Errorf("negative clamped Remaining = %d, want 1", b2.Remaining())
	}
}

func TestIterationBudget_RemainingAndRefund(t *testing.T) {
	b := NewIterationBudget(3)
	if b.Remaining() != 3 {
		t.Fatalf("Remaining = %d, want 3", b.Remaining())
	}
	b.Consume()
	if b.Remaining() != 2 {
		t.Errorf("after 1 consume Remaining = %d, want 2", b.Remaining())
	}
	b.Refund()
	if b.Remaining() != 3 {
		t.Errorf("after refund Remaining = %d, want 3", b.Remaining())
	}
}

func TestIterationBudget_ConsumeAtZero(t *testing.T) {
	b := NewIterationBudget(1)
	if !b.Consume() {
		t.Fatal("first consume should succeed")
	}
	if b.Remaining() != 0 {
		t.Fatalf("Remaining = %d, want 0", b.Remaining())
	}
	if b.Consume() {
		t.Error("consume at zero should fail")
	}
}

func TestDefaultBudgetConstants(t *testing.T) {
	if ParentIterationBudget != 90 || SubagentIterationBudget != 50 {
		t.Errorf("unexpected default budgets: parent=%d subagent=%d",
			ParentIterationBudget, SubagentIterationBudget)
	}
}

func TestCanonicalArgs_Empty(t *testing.T) {
	if got := canonicalArgs(nil); got != "" {
		t.Errorf("canonicalArgs(nil) = %q, want empty", got)
	}
	if got := canonicalArgs(map[string]any{}); got != "" {
		t.Errorf("canonicalArgs(empty) = %q, want empty", got)
	}
}

func TestSignature_DiffersByToolAndArgs(t *testing.T) {
	a := Signature("read", map[string]any{"path": "x"})
	b := Signature("write", map[string]any{"path": "x"})
	if a == b {
		t.Error("different tool names should yield different signatures")
	}
	c := Signature("read", map[string]any{"path": "y"})
	if a == c {
		t.Error("different args should yield different signatures")
	}
	if len(a) != 16 {
		t.Errorf("signature length = %d, want 16", len(a))
	}
}

// A window that never reaches repeatCap and is not a ping-pong returns false.
func TestLoopDetector_WindowEviction(t *testing.T) {
	d := NewLoopDetector()
	// Feed 8 distinct signatures; window (size 6) evicts oldest, never loops.
	sigs := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	for _, s := range sigs {
		if looping, _ := d.Record(s); looping {
			t.Errorf("distinct sigs should not loop at %q", s)
		}
	}
}

func TestLoopDetector_RepeatReason(t *testing.T) {
	d := NewLoopDetector()
	sig := Signature("read", map[string]any{"path": "same"})
	var reason string
	for i := 0; i < 3; i++ {
		_, reason = d.Record(sig)
	}
	if reason != "repeated identical tool call" {
		t.Errorf("reason = %q, want repeated identical tool call", reason)
	}
}
