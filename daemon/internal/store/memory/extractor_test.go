package memory

import (
	"testing"
)

func TestClassifyConfidence_HighAutoSave(t *testing.T) {
	entries := []MemoryEntry{
		{ID: "m1", Confidence: 0.95, Content: "high confidence fact", Type: MemoryPersonFact},
		{ID: "m2", Confidence: 0.85, Content: "borderline high", Type: MemoryUserTrait},
		{ID: "m3", Confidence: 0.86, Content: "just above threshold", Type: MemoryContext},
	}

	decision := ClassifyConfidence(entries)

	if len(decision.HighConfidence) != 3 {
		t.Errorf("HighConfidence len = %d, want 3 (all >= 0.85)", len(decision.HighConfidence))
	}
	if len(decision.MediumConfidence) != 0 {
		t.Errorf("MediumConfidence len = %d, want 0", len(decision.MediumConfidence))
	}
	if len(decision.LowConfidence) != 0 {
		t.Errorf("LowConfidence len = %d, want 0", len(decision.LowConfidence))
	}
}

func TestClassifyConfidence_MediumFlagForConfirmation(t *testing.T) {
	// Each entry sits in its type's medium band: 0.60 <= c < type-threshold.
	entries := []MemoryEntry{
		{ID: "m1", Confidence: 0.70, Content: "medium fact", Type: MemoryRule},          // rule threshold 0.75
		{ID: "m2", Confidence: 0.60, Content: "borderline low", Type: MemoryPersonFact}, // person_fact threshold 0.80
		{ID: "m3", Confidence: 0.79, Content: "just below high", Type: MemoryContext},   // context threshold 0.80
	}

	decision := ClassifyConfidence(entries)

	if len(decision.MediumConfidence) != 3 {
		t.Errorf("MediumConfidence len = %d, want 3 (all in per-type medium band)", len(decision.MediumConfidence))
	}
	if len(decision.HighConfidence) != 0 {
		t.Errorf("HighConfidence len = %d, want 0", len(decision.HighConfidence))
	}
	if len(decision.LowConfidence) != 0 {
		t.Errorf("LowConfidence len = %d, want 0", len(decision.LowConfidence))
	}
}

// TestClassifyConfidence_PerTypeThresholds verifies BUG 1's fix: the same
// confidence classifies differently depending on the entry's type.
func TestClassifyConfidence_PerTypeThresholds(t *testing.T) {
	entries := []MemoryEntry{
		// 0.78: above rule bar (0.75) -> High; below person_fact (0.80) -> Medium.
		{ID: "rule78", Confidence: 0.78, Type: MemoryRule},
		{ID: "pf78", Confidence: 0.78, Type: MemoryPersonFact},
		// 0.82: above person_fact/context (0.80) -> High; below user_trait (0.85) -> Medium.
		{ID: "ctx82", Confidence: 0.82, Type: MemoryContext},
		{ID: "ut82", Confidence: 0.82, Type: MemoryUserTrait},
	}

	decision := ClassifyConfidence(entries)

	high := map[string]bool{}
	for _, e := range decision.HighConfidence {
		high[e.ID] = true
	}
	medium := map[string]bool{}
	for _, e := range decision.MediumConfidence {
		medium[e.ID] = true
	}

	if !high["rule78"] {
		t.Error("rule@0.78 should auto-save (>= rule threshold 0.75)")
	}
	if !medium["pf78"] {
		t.Error("person_fact@0.78 should be medium (< person_fact threshold 0.80)")
	}
	if !high["ctx82"] {
		t.Error("context@0.82 should auto-save (>= context threshold 0.80)")
	}
	if !medium["ut82"] {
		t.Error("user_trait@0.82 should be medium (< user_trait threshold 0.85)")
	}
}

func TestClassifyConfidence_LowDiscard(t *testing.T) {
	entries := []MemoryEntry{
		{ID: "m1", Confidence: 0.30, Content: "low confidence", Type: MemoryPersonFact},
		{ID: "m2", Confidence: 0.59, Content: "just below medium", Type: MemoryRule},
		{ID: "m3", Confidence: 0.01, Content: "almost zero", Type: MemoryContext},
	}

	decision := ClassifyConfidence(entries)

	if len(decision.LowConfidence) != 3 {
		t.Errorf("LowConfidence len = %d, want 3 (all < 0.60)", len(decision.LowConfidence))
	}
	if len(decision.HighConfidence) != 0 {
		t.Errorf("HighConfidence len = %d, want 0", len(decision.HighConfidence))
	}
	if len(decision.MediumConfidence) != 0 {
		t.Errorf("MediumConfidence len = %d, want 0", len(decision.MediumConfidence))
	}
}

func TestClassifyConfidence_MixedThresholds(t *testing.T) {
	entries := []MemoryEntry{
		{ID: "h1", Confidence: 0.95, Content: "high"},
		{ID: "m1", Confidence: 0.75, Content: "medium"},
		{ID: "l1", Confidence: 0.50, Content: "low"},
		{ID: "h2", Confidence: 0.88, Content: "high2"},
		{ID: "m2", Confidence: 0.62, Content: "medium2"},
		{ID: "l2", Confidence: 0.10, Content: "low2"},
	}

	decision := ClassifyConfidence(entries)

	if len(decision.HighConfidence) != 2 {
		t.Errorf("HighConfidence len = %d, want 2", len(decision.HighConfidence))
	}
	if len(decision.MediumConfidence) != 2 {
		t.Errorf("MediumConfidence len = %d, want 2", len(decision.MediumConfidence))
	}
	if len(decision.LowConfidence) != 2 {
		t.Errorf("LowConfidence len = %d, want 2", len(decision.LowConfidence))
	}
}

func TestClassifyConfidence_Empty(t *testing.T) {
	decision := ClassifyConfidence(nil)

	if len(decision.HighConfidence) != 0 {
		t.Errorf("HighConfidence should be empty for nil input")
	}
	if len(decision.MediumConfidence) != 0 {
		t.Errorf("MediumConfidence should be empty for nil input")
	}
	if len(decision.LowConfidence) != 0 {
		t.Errorf("LowConfidence should be empty for nil input")
	}
}

func TestClassifyConfidence_SuggestedFlagSet(t *testing.T) {
	entries := []MemoryEntry{
		{ID: "h1", Confidence: 0.90, Content: "high", Type: MemoryPersonFact},
		{ID: "m1", Confidence: 0.75, Content: "medium", Type: MemoryPersonFact},
		{ID: "l1", Confidence: 0.50, Content: "low", Type: MemoryPersonFact},
	}

	decision := ClassifyConfidence(entries)

	// High: suggested=false (auto-confirmed)
	if decision.HighConfidence[0].Suggested {
		t.Error("High confidence entry should have Suggested=false")
	}

	// Medium: suggested=true (needs confirmation)
	if !decision.MediumConfidence[0].Suggested {
		t.Error("Medium confidence entry should have Suggested=true")
	}

	// Low: suggested=false (discarded anyway)
	if decision.LowConfidence[0].Suggested {
		t.Error("Low confidence entry should have Suggested=false")
	}
}

func TestFindConflicts_ContradictoryContent(t *testing.T) {
	existing := []MemoryEntry{
		{ID: "old1", Person: "Alice", Type: MemoryPersonFact, Content: "Alice hates coffee"},
	}

	new := []MemoryEntry{
		{ID: "new1", Person: "Alice", Type: MemoryPersonFact, Content: "Alice loves coffee"},
	}

	conflicts := FindConflicts(new, existing)

	if len(conflicts) != 1 {
		t.Fatalf("FindConflicts len = %d, want 1", len(conflicts))
	}
	if conflicts[0].Reason != "contradictory facts about Alice" {
		t.Errorf("conflict reason = %q, want 'contradictory facts about Alice'", conflicts[0].Reason)
	}
	if conflicts[0].New.Content != "Alice loves coffee" {
		t.Errorf("conflict New.Content = %q", conflicts[0].New.Content)
	}
	if conflicts[0].Existing.Content != "Alice hates coffee" {
		t.Errorf("conflict Existing.Content = %q", conflicts[0].Existing.Content)
	}
}

func TestFindConflicts_NoConflict(t *testing.T) {
	existing := []MemoryEntry{
		{ID: "old1", Person: "Alice", Type: MemoryPersonFact, Content: "Alice is a software engineer"},
	}

	new := []MemoryEntry{
		{ID: "new1", Person: "Alice", Type: MemoryPersonFact, Content: "Alice works at Acme Corp"},
		{ID: "new2", Person: "Bob", Type: MemoryUserTrait, Content: "Bob prefers async communication"},
	}

	conflicts := FindConflicts(new, existing)

	if len(conflicts) != 0 {
		t.Errorf("expected 0 conflicts, got %d", len(conflicts))
	}
}

func TestFindConflicts_DifferentPerson(t *testing.T) {
	existing := []MemoryEntry{
		{ID: "old1", Person: "Alice", Type: MemoryPersonFact, Content: "Alice likes coffee"},
	}

	new := []MemoryEntry{
		{ID: "new1", Person: "Bob", Type: MemoryPersonFact, Content: "Bob hates coffee"},
	}

	conflicts := FindConflicts(new, existing)

	if len(conflicts) != 0 {
		t.Errorf("expected 0 conflicts for different people, got %d", len(conflicts))
	}
}

func TestFindConflicts_EmptyInputs(t *testing.T) {
	conflicts := FindConflicts(nil, nil)
	if len(conflicts) != 0 {
		t.Errorf("FindConflicts(nil,nil) should return empty, got %d", len(conflicts))
	}

	conflicts = FindConflicts([]MemoryEntry{}, []MemoryEntry{})
	if len(conflicts) != 0 {
		t.Errorf("FindConflicts(empty,empty) should return empty, got %d", len(conflicts))
	}
}

// TestFindConflicts_TopicOverlapNoPolarity verifies BUG 3's fix: two facts about
// the same person/type that merely share a topic word (here "project") but express
// no opposing sentiment must NOT be flagged as contradictory.
func TestFindConflicts_TopicOverlapNoPolarity(t *testing.T) {
	existing := []MemoryEntry{
		{ID: "old1", Person: "Alice", Type: MemoryPersonFact, Content: "Alice leads the payments project"},
	}
	new := []MemoryEntry{
		{ID: "new1", Person: "Alice", Type: MemoryPersonFact, Content: "Alice joined the project last spring"},
	}

	conflicts := FindConflicts(new, existing)
	if len(conflicts) != 0 {
		t.Errorf("shared topic word without opposing polarity should not conflict, got %d", len(conflicts))
	}
}

func TestMockExtractor(t *testing.T) {
	mockEntries := []MemoryEntry{
		{ID: "mock1", Type: MemoryPersonFact, Person: "Alice", Content: "Alice is a senior engineer", Confidence: 0.95},
		{ID: "mock2", Type: MemoryUserTrait, Content: "Prefers async communication", Confidence: 0.70},
	}

	extractor := &MockExtractor{Entries: mockEntries}
	entries, err := extractor.Extract("User: Tell me about Alice\nAssistant: Alice is a senior engineer who prefers async communication.")
	if err != nil {
		t.Fatalf("MockExtractor.Extract: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("Extract len = %d, want 2", len(entries))
	}
	if entries[0].Person != "Alice" {
		t.Errorf("entries[0].Person = %q, want Alice", entries[0].Person)
	}
	if entries[1].Confidence != 0.70 {
		t.Errorf("entries[1].Confidence = %f, want 0.70", entries[1].Confidence)
	}
}

func TestMockExtractorReturnsSuggestedFlag(t *testing.T) {
	mockEntries := []MemoryEntry{
		{ID: "m1", Confidence: 0.95, Content: "high"},
		{ID: "m2", Confidence: 0.75, Content: "medium"},
		{ID: "m3", Confidence: 0.50, Content: "low"},
	}

	extractor := &MockExtractor{Entries: mockEntries}
	entries, err := extractor.Extract("test conversation")
	if err != nil {
		t.Fatalf("MockExtractor.Extract: %v", err)
	}

	// MockExtractor should NOT set Suggested (that's ClassifyConfidence's job).
	// The extractor just returns the raw LLM results.
	for _, e := range entries {
		if e.Suggested {
			t.Errorf("MockExtractor should not set Suggested on entry %q", e.ID)
		}
	}
}

func TestProcessConversation_Integration(t *testing.T) {
	// Full post-conversation pipeline: extract → classify → conflict-check.

	existingMemories := []MemoryEntry{
		{ID: "old1", Person: "Alice", Type: MemoryPersonFact, Content: "Alice dislikes meetings", Confidence: 0.90, Confirmed: true},
	}

	mockExtracted := []MemoryEntry{
		{ID: "new1", Type: MemoryPersonFact, Person: "Alice", Content: "Alice now enjoys team meetings as of Q3", Confidence: 0.88},
		{ID: "new2", Type: MemoryUserTrait, Content: "User prefers bullet-point summaries", Confidence: 0.72},
		{ID: "new3", Type: MemoryRule, Content: "Never schedule meetings on Friday afternoon", Confidence: 0.45},
	}

	extractor := &MockExtractor{Entries: mockExtracted}
	entries, err := extractor.Extract("conversation about meetings")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	decision := ClassifyConfidence(entries)

	// new1 (0.88): high confidence
	if len(decision.HighConfidence) != 1 || decision.HighConfidence[0].ID != "new1" {
		t.Errorf("HighConfidence should contain new1")
	}

	// new2 (0.72): medium confidence
	if len(decision.MediumConfidence) != 1 || decision.MediumConfidence[0].ID != "new2" {
		t.Errorf("MediumConfidence should contain new2")
	}

	// new3 (0.45): low confidence, discarded
	if len(decision.LowConfidence) != 1 || decision.LowConfidence[0].ID != "new3" {
		t.Errorf("LowConfidence should contain new3")
	}

	// Conflict detection: new1 ("Alice now enjoys team meetings") vs old1 ("Alice dislikes meetings")
	conflicts := FindConflicts(entries, existingMemories)
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(conflicts))
	}
	if conflicts[0].New.ID != "new1" {
		t.Errorf("conflict New.ID = %q, want new1", conflicts[0].New.ID)
	}
	if conflicts[0].Existing.ID != "old1" {
		t.Errorf("conflict Existing.ID = %q, want old1", conflicts[0].Existing.ID)
	}
}
