package tool

import "testing"

func TestLoopDetectorRepeat(t *testing.T) {
	d := NewLoopDetector()
	sig := Signature("read", map[string]any{"path": "a.txt"})
	looping := false
	for i := 0; i < 3; i++ {
		l, _ := d.Record(sig)
		looping = l
	}
	if !looping {
		t.Errorf("3 identical calls should trip the repeat detector")
	}
}

func TestLoopDetectorPingPong(t *testing.T) {
	d := NewLoopDetector()
	a := Signature("read", map[string]any{"path": "a"})
	b := Signature("read", map[string]any{"path": "b"})
	seq := []string{a, b, a, b}
	var looping bool
	var reason string
	for _, s := range seq {
		looping, reason = d.Record(s)
	}
	if !looping || reason != "ping-pong between two tool calls" {
		t.Errorf("A,B,A,B should trip ping-pong; got looping=%v reason=%q", looping, reason)
	}
}

func TestLoopDetectorNoFalsePositive(t *testing.T) {
	d := NewLoopDetector()
	calls := []string{
		Signature("read", map[string]any{"path": "a"}),
		Signature("write", map[string]any{"path": "b"}),
		Signature("websearch", map[string]any{"q": "x"}),
		Signature("read", map[string]any{"path": "c"}),
	}
	for _, s := range calls {
		if l, _ := d.Record(s); l {
			t.Errorf("distinct progressing calls should not trip the detector")
		}
	}
}

func TestSignatureStableAcrossArgOrder(t *testing.T) {
	s1 := Signature("t", map[string]any{"a": 1, "b": 2})
	s2 := Signature("t", map[string]any{"b": 2, "a": 1})
	if s1 != s2 {
		t.Errorf("signature must be order-independent: %s vs %s", s1, s2)
	}
}

func TestIterationBudget(t *testing.T) {
	b := NewIterationBudget(2)
	if !b.Consume() || !b.Consume() {
		t.Fatal("first two consumes should succeed")
	}
	if b.Consume() {
		t.Error("third consume should fail (budget exhausted)")
	}
	b.Refund()
	if !b.Consume() {
		t.Error("consume after refund should succeed")
	}
}
