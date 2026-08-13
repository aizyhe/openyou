package server

import "testing"

func TestParseCurationsFiltersHallucinated(t *testing.T) {
	valid := map[string]bool{"workplace-advisor": true, "workplace-politics": true}
	resp := `[
	  {"action":"merge","skills":["workplace-advisor","workplace-politics"],"reason":"overlap"},
	  {"action":"archive","skills":["nonexistent-skill"],"reason":"stale"}
	]`
	got := parseCurations(resp, valid)
	if len(got) != 1 {
		t.Fatalf("hallucinated-only suggestion should be dropped; got %d", len(got))
	}
	if got[0].Action != "merge" || len(got[0].Skills) != 2 {
		t.Errorf("unexpected: %+v", got[0])
	}
}

func TestParseCurationsPartialHallucination(t *testing.T) {
	valid := map[string]bool{"a": true}
	resp := `[{"action":"merge","skills":["a","ghost"],"reason":"x"}]`
	got := parseCurations(resp, valid)
	if len(got) != 1 || len(got[0].Skills) != 1 || got[0].Skills[0] != "a" {
		t.Errorf("should keep valid names only, got %+v", got)
	}
}

func TestParseCurationsEmpty(t *testing.T) {
	if got := parseCurations("[]", map[string]bool{}); len(got) != 0 {
		t.Errorf("empty array should yield no suggestions, got %+v", got)
	}
	if got := parseCurations("garbage", map[string]bool{}); got != nil {
		t.Errorf("invalid JSON should yield nil, got %+v", got)
	}
}
