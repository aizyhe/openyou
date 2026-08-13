package privacy

import (
	"strconv"
	"strings"
	"testing"
)

func TestNewEmptyNames(t *testing.T) {
	p := New(nil)
	if p == nil {
		t.Fatal("New(nil) should return a non-nil Pseudonymizer")
	}
	// Mask/Unmask on empty pseudonymizer should be no-ops.
	text := "Alice said hello to Bob"
	if got := p.Mask(text); got != text {
		t.Errorf("Mask with no names should return original text, got %q", got)
	}
	if got := p.Unmask("Engineer A said hello"); got != "Engineer A said hello" {
		t.Errorf("Unmask with no names should return original text, got %q", got)
	}
}

func TestNewWithNames(t *testing.T) {
	names := []string{"Alice", "Bob", "Charlie"}
	p := New(names)

	// Verify Mask replaces names.
	result := p.Mask("Alice and Bob discussed with Charlie")
	if result == "Alice and Bob discussed with Charlie" {
		t.Error("Mask should replace real names with pseudonyms")
	}
	// Real names should be gone.
	for _, name := range names {
		for i := 0; i < len(result)-len(name)+1; i++ {
			if result[i:i+len(name)] == name {
				t.Errorf("Mask output still contains real name %q: %s", name, result)
			}
		}
	}
	// Pseudonyms should be present (sentinel 〔代号N〕 form).
	engCount := strings.Count(result, "〔代号")
	if engCount < 2 {
		t.Errorf("Expected at least 2 sentinel pseudonyms, got %d in: %s", engCount, result)
	}
}

// TestUnmaskDoesNotFalselyRewriteModelText guards the false-unmask bug: the
// sentinel codename must not collide with ordinary text the model emits on its
// own, so Unmask never fabricates a real person into unrelated output.
func TestUnmaskDoesNotFalselyRewriteModelText(t *testing.T) {
	p := New([]string{"张伟", "李明"})
	// The model spontaneously writes English that contains "Engineer A" — the
	// OLD codename scheme. With sentinel codenames this must pass through.
	in := "You should hire an Engineer A for this role."
	if got := p.Unmask(in); got != in {
		t.Errorf("Unmask must not rewrite model's own words:\n  in:  %q\n  got: %q", in, got)
	}
	// Sanity: a genuine sentinel IS still unmasked.
	masked := p.Mask("张伟 领导团队")
	if unmasked := p.Unmask(masked); unmasked != "张伟 领导团队" {
		t.Errorf("genuine sentinel should round-trip, got %q", unmasked)
	}
}

func TestMaskPartialNameMatch(t *testing.T) {
	// Ensure partial substrings of names are not replaced.
	names := []string{"Al"}
	p := New(names)

	// "Al" should be replaced in "Al went home"
	result := p.Mask("Al went home")
	if result == "Al went home" {
		t.Error("Mask should replace 'Al'")
	}

	// Mask replaces ALL occurrences including substrings ("Al" inside "Alice"):
	// strings.ReplaceAll matches substrings, not whole words — known limitation,
	// documented here.
	p2 := New([]string{"Al"})
	result2 := p2.Mask("Alice went to the mall")
	// "Al" appears in "Alice" and "mall" — both get pseudonym fragments.
	if result2 == "Alice went to the mall" {
		t.Error("Mask with substring name should replace occurrences")
	}
}

func TestMaskNoMatch(t *testing.T) {
	names := []string{"Alice", "Bob"}
	p := New(names)

	text := "Charlie went to the store"
	result := p.Mask(text)
	if result != text {
		t.Errorf("Mask of text with no known names should be unchanged, got %q", result)
	}
}

func TestMaskEmptyText(t *testing.T) {
	names := []string{"Alice"}
	p := New(names)

	result := p.Mask("")
	if result != "" {
		t.Errorf("Mask of empty text should be empty, got %q", result)
	}
}

func TestUnmaskRoundtrip(t *testing.T) {
	names := []string{"Alice", "Bob", "Charlie"}
	p := New(names)

	original := "Alice met Bob and asked about Charlie's project"
	masked := p.Mask(original)
	unmasked := p.Unmask(masked)

	if unmasked != original {
		t.Errorf("Roundtrip failed:\n  original: %q\n  masked:   %q\n  unmasked: %q",
			original, masked, unmasked)
	}
}

func TestUnmaskComplexText(t *testing.T) {
	names := []string{"Alice", "Bob"}
	p := New(names)

	text := "Alice: I agree with Bob\nBob: Thanks Alice"
	masked := p.Mask(text)

	// Engineer pseudonyms should be in the masked text.
	if masked == text {
		t.Error("Mask should have replaced names")
	}

	// Roundtrip should restore.
	unmasked := p.Unmask(masked)
	if unmasked != text {
		t.Errorf("Unmask did not restore original:\n  expected: %q\n  got:      %q", text, unmasked)
	}
}

func TestMaskUnmaskWithRepeatedNames(t *testing.T) {
	names := []string{"Alice"}
	p := New(names)

	text := "Alice said Alice would handle Alice's tasks"
	masked := p.Mask(text)

	// All three "Alice" should be replaced.
	for i := 0; i < len(masked)-len("Alice")+1; i++ {
		if masked[i:i+len("Alice")] == "Alice" {
			t.Errorf("Masked text still contains 'Alice': %s", masked)
		}
	}

	// Unmask should restore all three.
	unmasked := p.Unmask(masked)
	if unmasked != text {
		t.Errorf("Roundtrip with repeated names failed:\n  expected: %q\n  got:      %q", text, unmasked)
	}
}

func TestUpdateNames(t *testing.T) {
	names := []string{"Alice", "Bob"}
	p := New(names)

	// Original mapping: Alice=Engineer A, Bob=Engineer B.
	masked := p.Mask("Alice spoke to Bob")
	if masked == "Alice spoke to Bob" {
		t.Error("Mask should replace names before update")
	}

	// Update to new names.
	newNames := []string{"Charlie", "Diana"}
	p.UpdateNames(newNames)

	// Old names should no longer be masked.
	result := p.Mask("Alice spoke to Bob")
	if result != "Alice spoke to Bob" {
		t.Errorf("After UpdateNames, old names should not be masked: got %q", result)
	}

	// New names should be masked.
	result2 := p.Mask("Charlie spoke to Diana")
	if result2 == "Charlie spoke to Diana" {
		t.Errorf("After UpdateNames, new names should be masked: got %q", result2)
	}

	// Unmask should use the new mapping.
	unmasked := p.Unmask(result2)
	if unmasked != "Charlie spoke to Diana" {
		t.Errorf("Unmask after UpdateNames should restore new names: got %q", unmasked)
	}
}

func TestManyNames(t *testing.T) {
	// 14 names / 14 labels (A-N): overflow wraps around (name[0] and name[14]
	// both map to "Engineer A"), so roundtrip is lossy past the label count —
	// known limitation.
	names := []string{
		"N1", "N2", "N3", "N4", "N5", "N6", "N7",
		"N8", "N9", "N10", "N11", "N12", "N13", "N14", "N15",
	}
	p := New(names)

	// All names should be masked.
	text := "N1 N2 N3 N4 N5 N6 N7 N8 N9 N10 N11 N12 N13 N14 N15"
	result := p.Mask(text)
	if result == text {
		t.Error("Mask with many names should replace all names")
	}

	// Verify each real name is gone from the output.
	for _, name := range names {
		// Check that this exact name string doesn't appear in result.
		// Use a simple loop rather than strings.Contains since we need
		// to verify exact name no longer appears.
		idx := 0
		for idx+len(name) <= len(result) {
			if result[idx:idx+len(name)] == name {
				t.Errorf("Mask output still contains %q: %s", name, result)
				break
			}
			idx++
		}
	}

	// Roundtrip is only guaranteed when names fit within labels (<= 14).
	// For the first 14 names, Unmask should restore correctly.
	// We use 14 unique names to validate roundtrip.
	p14 := New(names[:14])
	text14 := "N1 N2 N3 N4 N5 N6 N7 N8 N9 N10 N11 N12 N13 N14"
	masked14 := p14.Mask(text14)
	unmasked14 := p14.Unmask(masked14)
	if unmasked14 != text14 {
		t.Errorf("14-name roundtrip failed:\n  expected: %q\n  got:      %q", text14, unmasked14)
	}
}

func TestManyNamesUniquePseudonyms(t *testing.T) {
	// BUG 1: with more people than labels, every real name must still get a
	// UNIQUE pseudonym and round-trip Mask→Unmask correctly.
	const n = 30 // > 26, exercises the numeric-suffix overflow scheme
	names := make([]string, n)
	for i := range names {
		names[i] = "Person" + strconv.Itoa(i)
	}
	p := New(names)

	// Every real name maps to a distinct pseudonym (no collisions).
	seen := make(map[string]string)
	for _, name := range names {
		pseudo := p.Mask(name)
		if pseudo == name {
			t.Fatalf("name %q was not masked", name)
		}
		if prev, ok := seen[pseudo]; ok {
			t.Fatalf("pseudonym collision: %q and %q both map to %q", prev, name, pseudo)
		}
		seen[pseudo] = name
	}

	// Round-trip a text containing all names in one shot.
	var parts []string
	for i := range names {
		parts = append(parts, names[i])
	}
	text := strings.Join(parts, " | ")
	masked := p.Mask(text)
	if unmasked := p.Unmask(masked); unmasked != text {
		t.Errorf(">26-name roundtrip failed:\n  expected: %q\n  got:      %q", text, unmasked)
	}
}

func TestUpdateNamesUniquePseudonyms(t *testing.T) {
	// BUG 1: UpdateNames must use the same non-colliding scheme as New.
	const n = 30
	names := make([]string, n)
	for i := range names {
		names[i] = "Contact" + strconv.Itoa(i)
	}
	p := New(nil)
	p.UpdateNames(names)

	text := strings.Join(names, " ")
	masked := p.Mask(text)
	if unmasked := p.Unmask(masked); unmasked != text {
		t.Errorf("UpdateNames >26-name roundtrip failed:\n  expected: %q\n  got:      %q", text, unmasked)
	}
}

func TestMaskSubstringNames(t *testing.T) {
	// BUG 2: one name being a substring of another must not corrupt the
	// longer name, and the result must be deterministic.
	names := []string{"张建", "张建国", "李", "李明"}
	p := New(names)

	text := "张建国 和 张建 讨论，李明 找 李 帮忙"
	masked := p.Mask(text)

	// No real name should survive in the masked output.
	for _, name := range names {
		if strings.Contains(masked, name) {
			t.Errorf("masked text still contains real name %q: %s", name, masked)
		}
	}

	// Round-trip must restore the exact original.
	if unmasked := p.Unmask(masked); unmasked != text {
		t.Errorf("substring-name roundtrip failed:\n  expected: %q\n  got:      %q", text, unmasked)
	}

	// Deterministic: masking the same text repeatedly yields the same output
	// (the old map-iteration-order bug made this nondeterministic).
	for i := 0; i < 20; i++ {
		if again := p.Mask(text); again != masked {
			t.Fatalf("Mask is nondeterministic:\n  first: %q\n  again: %q", masked, again)
		}
	}
}

func TestUnmaskTextWithoutPseudonyms(t *testing.T) {
	names := []string{"Alice"}
	p := New(names)

	text := "This text has no pseudonyms"
	result := p.Unmask(text)
	if result != text {
		t.Errorf("Unmask of text without pseudonyms should be unchanged, got %q", result)
	}
}

func TestConcurrentMaskUnmask(t *testing.T) {
	names := []string{"Alice", "Bob", "Charlie", "Diana", "Eve"}
	p := New(names)

	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				text := "Alice and Bob met Charlie"
				masked := p.Mask(text)
				if masked == text {
					t.Error("Mask should replace names in concurrent access")
				}
				unmasked := p.Unmask(masked)
				if unmasked != text {
					t.Errorf("Concurrent roundtrip failed: got %q", unmasked)
				}
			}
			done <- true
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestUpdateNamesConcurrent(t *testing.T) {
	names := []string{"Alice", "Bob"}
	p := New(names)

	done := make(chan bool, 10)
	for i := 0; i < 5; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				p.Mask("Alice met Bob")
			}
			done <- true
		}()
	}
	for i := 0; i < 5; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				p.UpdateNames([]string{"Charlie", "Diana"})
			}
			done <- true
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	// No panic = pass.
}

func TestSetEnabledAndEnabled(t *testing.T) {
	p := New([]string{"Alice"})
	if !p.Enabled() {
		t.Error("New should start enabled")
	}
	// Disable → Mask/Unmask become pass-through.
	p.SetEnabled(false)
	if p.Enabled() {
		t.Error("Enabled() should report false after SetEnabled(false)")
	}
	if got := p.Mask("Alice"); got != "Alice" {
		t.Errorf("disabled Mask should pass through, got %q", got)
	}
	if got := p.Unmask("Engineer A"); got != "Engineer A" {
		t.Errorf("disabled Unmask should pass through, got %q", got)
	}
	// Re-enable → masking resumes.
	p.SetEnabled(true)
	if got := p.Mask("Alice"); got == "Alice" {
		t.Error("re-enabled Mask should replace names")
	}
}

func TestMaskWindow(t *testing.T) {
	// Empty → floor of 16.
	if got := New(nil).MaskWindow(); got != 16 {
		t.Errorf("empty MaskWindow = %d, want 16", got)
	}
	// A long real name exceeds the floor and the codename length.
	long := "ThisIsAVeryLongRealNameExceedingSixteen"
	p := New([]string{long})
	if got := p.MaskWindow(); got != len(long) {
		t.Errorf("MaskWindow = %d, want %d (len of long name)", got, len(long))
	}
	// Short names keep the 16 floor.
	if got := New([]string{"Al"}).MaskWindow(); got != 16 {
		t.Errorf("short-name MaskWindow = %d, want 16", got)
	}
}

// Pins the C16 fix: codenames are positional (〔代号N〕), so reseeding mid-stream
// shifts indices and unmasking would hit the wrong people — a request-start
// snapshot must stay frozen.
func TestSnapshotStableAcrossReseed(t *testing.T) {
	p := New([]string{"张伟", "李明"})
	snap := p.Snapshot()

	masked := snap.Mask("张伟和李明开会")
	if masked == "张伟和李明开会" {
		t.Fatalf("snapshot Mask did not pseudonymize: %q", masked)
	}

	// Reseed the LIVE pseudonymizer with a reordered/extended list. Positional
	// indices now mean different people on the live map.
	p.UpdateNames([]string{"王五", "李明", "张伟"})

	// The snapshot must still unmask to the ORIGINAL people it masked with.
	if got := snap.Unmask(masked); got != "张伟和李明开会" {
		t.Errorf("snapshot unmask drifted after live reseed: got %q", got)
	}
	// The live map, by contrast, resolves the same names differently now —
	// proving the drift the snapshot protects against is real.
	if live := p.Mask("张伟"); live == snap.Mask("张伟") {
		t.Errorf("expected live map to drift from snapshot after reseed (live=%q snap=%q)", live, snap.Mask("张伟"))
	}
	// Snapshot masking stays frozen at the original assignment.
	if got := snap.Mask("张伟"); got != "〔代号1〕" {
		t.Errorf("snapshot Mask drifted: got %q, want 〔代号1〕", got)
	}
}
