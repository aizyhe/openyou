package memory

import (
	"encoding/json"
	"testing"
)

func TestMemoryTypeConstants(t *testing.T) {
	tests := []struct {
		name    string
		mt      MemoryType
		wantStr string
	}{
		{"person_fact", MemoryPersonFact, "person_fact"},
		{"user_trait", MemoryUserTrait, "user_trait"},
		{"rule", MemoryRule, "rule"},
		{"context", MemoryContext, "context"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.mt) != tt.wantStr {
				t.Errorf("MemoryType %q: got string %q, want %q", tt.name, string(tt.mt), tt.wantStr)
			}
		})
	}
}

func TestMemoryEntryJSONRoundtrip(t *testing.T) {
	entry := MemoryEntry{
		ID:             "mem_abc123",
		Type:           MemoryPersonFact,
		Person:         "Alice",
		Content:        "Alice recently started focusing on cost control",
		Confidence:     0.92,
		SourceSession:  "ses_xyz789",
		Confirmed:      false,
		Suggested:      true,
		DecayRate:      0.01,
		RelevanceScore: 0.85,
		Tags:           []string{"alice", "finance"},
		CreatedAt:      1720000000000000000,
		UpdatedAt:      1720000000000000000,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded MemoryEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if decoded.ID != entry.ID {
		t.Errorf("ID = %q, want %q", decoded.ID, entry.ID)
	}
	if decoded.Type != entry.Type {
		t.Errorf("Type = %q, want %q", decoded.Type, entry.Type)
	}
	if decoded.Person != entry.Person {
		t.Errorf("Person = %q, want %q", decoded.Person, entry.Person)
	}
	if decoded.Content != entry.Content {
		t.Errorf("Content = %q, want %q", decoded.Content, entry.Content)
	}
	if decoded.Confidence != entry.Confidence {
		t.Errorf("Confidence = %f, want %f", decoded.Confidence, entry.Confidence)
	}
	if decoded.SourceSession != entry.SourceSession {
		t.Errorf("SourceSession = %q, want %q", decoded.SourceSession, entry.SourceSession)
	}
	if decoded.Confirmed != entry.Confirmed {
		t.Errorf("Confirmed = %v, want %v", decoded.Confirmed, entry.Confirmed)
	}
	if decoded.Suggested != entry.Suggested {
		t.Errorf("Suggested = %v, want %v", decoded.Suggested, entry.Suggested)
	}
	if decoded.DecayRate != entry.DecayRate {
		t.Errorf("DecayRate = %f, want %f", decoded.DecayRate, entry.DecayRate)
	}
	if decoded.RelevanceScore != entry.RelevanceScore {
		t.Errorf("RelevanceScore = %f, want %f", decoded.RelevanceScore, entry.RelevanceScore)
	}
	if len(decoded.Tags) != 2 || decoded.Tags[0] != "alice" || decoded.Tags[1] != "finance" {
		t.Errorf("Tags = %v, want [alice finance]", decoded.Tags)
	}
	if decoded.CreatedAt != entry.CreatedAt {
		t.Errorf("CreatedAt = %d, want %d", decoded.CreatedAt, entry.CreatedAt)
	}
	if decoded.UpdatedAt != entry.UpdatedAt {
		t.Errorf("UpdatedAt = %d, want %d", decoded.UpdatedAt, entry.UpdatedAt)
	}
}

func TestNewMemoryEntryDefaults(t *testing.T) {
	entry := NewMemoryEntry(MemoryPersonFact, "Alice likes coffee", 0.90, "ses_001")

	if entry.Type != MemoryPersonFact {
		t.Errorf("Type = %q, want %q", entry.Type, MemoryPersonFact)
	}
	if entry.Content != "Alice likes coffee" {
		t.Errorf("Content = %q", entry.Content)
	}
	if entry.Confidence != 0.90 {
		t.Errorf("Confidence = %f, want 0.90", entry.Confidence)
	}
	if entry.SourceSession != "ses_001" {
		t.Errorf("SourceSession = %q, want ses_001", entry.SourceSession)
	}
	if entry.DecayRate != 0.01 {
		t.Errorf("NewMemoryEntry DecayRate = %f, want 0.01", entry.DecayRate)
	}
	if entry.RelevanceScore != 0.5 {
		t.Errorf("NewMemoryEntry RelevanceScore = %f, want 0.5", entry.RelevanceScore)
	}
	if entry.Confirmed {
		t.Error("NewMemoryEntry Confirmed should default to false")
	}
	if entry.Suggested {
		t.Error("NewMemoryEntry Suggested should default to false")
	}
	if len(entry.Tags) != 0 {
		t.Errorf("NewMemoryEntry Tags should be empty, got %v", entry.Tags)
	}
	if entry.ID != "" {
		t.Errorf("NewMemoryEntry ID should be empty before save, got %q", entry.ID)
	}
	if entry.CreatedAt != 0 {
		t.Errorf("NewMemoryEntry CreatedAt should be zero before save, got %d", entry.CreatedAt)
	}
}

func TestConfidenceDecisionStruct(t *testing.T) {
	high := MemoryEntry{ID: "h1", Confidence: 0.90}
	medium := MemoryEntry{ID: "m1", Confidence: 0.75}
	low := MemoryEntry{ID: "l1", Confidence: 0.50}
	conflict := Conflict{
		New:      MemoryEntry{ID: "new1", Content: "Alice likes coffee"},
		Existing: MemoryEntry{ID: "old1", Content: "Alice hates coffee"},
		Reason:   "contradictory facts about Alice's coffee preference",
	}

	cd := ConfidenceDecision{
		HighConfidence:   []MemoryEntry{high},
		MediumConfidence: []MemoryEntry{medium},
		LowConfidence:    []MemoryEntry{low},
		Conflicts:        []Conflict{conflict},
	}

	if len(cd.HighConfidence) != 1 {
		t.Errorf("HighConfidence len = %d, want 1", len(cd.HighConfidence))
	}
	if len(cd.MediumConfidence) != 1 {
		t.Errorf("MediumConfidence len = %d, want 1", len(cd.MediumConfidence))
	}
	if len(cd.LowConfidence) != 1 {
		t.Errorf("LowConfidence len = %d, want 1", len(cd.LowConfidence))
	}
	if len(cd.Conflicts) != 1 {
		t.Errorf("Conflicts len = %d, want 1", len(cd.Conflicts))
	}
	if cd.Conflicts[0].Reason != "contradictory facts about Alice's coffee preference" {
		t.Errorf("Conflict reason = %q", cd.Conflicts[0].Reason)
	}
}
