package matcher

import (
	"testing"
)

// C1: tailToFit returns "" when the first line alone exceeds the budget, so the
// caller never injects an over-budget chunk.
func TestTailToFit_FirstLineOverflow(t *testing.T) {
	// A single over-long line with len > max
	if got := tailToFit("这一行非常非常长超出预算很长很长很长很长很长很长很长很长很长很长", 30); got != "" {
		t.Errorf("first line exceeding max should return empty, got %q", got)
	}
	// Multiple lines: keep the whole lines that fit
	text := "行一\n行二行二\n行三行三行三\n"
	got := tailToFit(text, 14)
	if got == "" {
		t.Fatal("should keep at least the first line")
	}
	if len([]rune(got)) > 14 {
		t.Errorf("result exceeds budget: %q (%d runes)", got, len([]rune(got)))
	}
}
