package memory

import (
	"database/sql"
	"math"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// One connection: additional pool connections to :memory: are separate
	// empty databases and intermittently fail with "no such table".
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func TestSaveAndGetMemory(t *testing.T) {
	store := newTestStore(t)

	entry := NewMemoryEntry(MemoryPersonFact, "Alice recently started focusing on cost control", 0.92, "ses_001")
	entry.Person = "Alice"
	entry.Tags = []string{"alice", "finance"}

	saved, err := store.Save(entry)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.ID == "" {
		t.Error("Save should generate a non-empty ID")
	}
	if saved.CreatedAt == 0 {
		t.Error("Save should set CreatedAt timestamp")
	}
	if saved.UpdatedAt == 0 {
		t.Error("Save should set UpdatedAt timestamp")
	}
	if saved.CreatedAt != saved.UpdatedAt {
		t.Error("created_at should equal updated_at for new entry")
	}

	got, ok := store.Get(saved.ID)
	if !ok {
		t.Fatal("Get returned false for saved memory")
	}
	if got.ID != saved.ID {
		t.Errorf("ID = %q, want %q", got.ID, saved.ID)
	}
	if got.Type != MemoryPersonFact {
		t.Errorf("Type = %q, want %q", got.Type, MemoryPersonFact)
	}
	if got.Person != "Alice" {
		t.Errorf("Person = %q, want Alice", got.Person)
	}
	if got.Content != "Alice recently started focusing on cost control" {
		t.Errorf("Content mismatch: %q", got.Content)
	}
	if got.Confidence != 0.92 {
		t.Errorf("Confidence = %f, want 0.92", got.Confidence)
	}
	if got.SourceSession != "ses_001" {
		t.Errorf("SourceSession = %q, want ses_001", got.SourceSession)
	}
	if len(got.Tags) != 2 {
		t.Errorf("Tags len = %d, want 2", len(got.Tags))
	}
}

func TestGetMemoryNotFound(t *testing.T) {
	store := newTestStore(t)

	_, ok := store.Get("mem_nonexistent")
	if ok {
		t.Error("Get should return false for nonexistent memory")
	}
}

// TestSaveUpsertExistingID verifies BUG 2's fix: re-saving an entry whose ID
// already exists updates the row (upsert) instead of failing with a UNIQUE
// constraint error.
func TestSaveUpsertExistingID(t *testing.T) {
	store := newTestStore(t)

	entry := NewMemoryEntry(MemoryPersonFact, "Alice likes coffee", 0.70, "ses_001")
	entry.Person = "Alice"
	saved, err := store.Save(entry)
	if err != nil {
		t.Fatalf("initial Save: %v", err)
	}

	// Re-save with the same ID but changed content/confidence.
	updated := saved
	updated.Content = "Alice loves espresso"
	updated.Confidence = 0.95
	resaved, err := store.Save(updated)
	if err != nil {
		t.Fatalf("re-Save with existing ID should upsert, not error: %v", err)
	}
	if resaved.ID != saved.ID {
		t.Errorf("upsert changed ID: got %q, want %q", resaved.ID, saved.ID)
	}
	if resaved.UpdatedAt < saved.UpdatedAt {
		t.Error("upsert should refresh updated_at")
	}

	got, ok := store.Get(saved.ID)
	if !ok {
		t.Fatal("Get returned false after upsert")
	}
	if got.Content != "Alice loves espresso" {
		t.Errorf("Content = %q, want updated value", got.Content)
	}
	if got.Confidence != 0.95 {
		t.Errorf("Confidence = %f, want 0.95", got.Confidence)
	}
	if got.CreatedAt != saved.CreatedAt {
		t.Errorf("CreatedAt should be preserved on upsert: got %d, want %d", got.CreatedAt, saved.CreatedAt)
	}

	// Ensure exactly one row exists (updated, not inserted twice).
	all, err := store.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 row after upsert, got %d", len(all))
	}
}

func TestListAllMemories(t *testing.T) {
	store := newTestStore(t)

	entries := []MemoryEntry{
		{Content: "Memory 1", Type: MemoryPersonFact, Confidence: 0.90, SourceSession: "ses_001"},
		{Content: "Memory 2", Type: MemoryUserTrait, Confidence: 0.95, SourceSession: "ses_002"},
		{Content: "Memory 3", Type: MemoryRule, Confidence: 0.80, SourceSession: "ses_003"},
	}
	for i := range entries {
		entries[i].Tags = []string{}
		if _, err := store.Save(entries[i]); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}

	all, err := store.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("ListAll len = %d, want 3", len(all))
	}
}

func TestListByType(t *testing.T) {
	store := newTestStore(t)

	store.Save(NewMemoryEntry(MemoryPersonFact, "p1", 0.90, "ses_001"))
	store.Save(NewMemoryEntry(MemoryPersonFact, "p2", 0.80, "ses_002"))
	store.Save(NewMemoryEntry(MemoryUserTrait, "t1", 0.95, "ses_001"))

	personFacts, err := store.ListByType(MemoryPersonFact)
	if err != nil {
		t.Fatalf("ListByType person_fact: %v", err)
	}
	if len(personFacts) != 2 {
		t.Errorf("person_fact count = %d, want 2", len(personFacts))
	}

	userTraits, err := store.ListByType(MemoryUserTrait)
	if err != nil {
		t.Fatalf("ListByType user_trait: %v", err)
	}
	if len(userTraits) != 1 {
		t.Errorf("user_trait count = %d, want 1", len(userTraits))
	}

	rules, err := store.ListByType(MemoryRule)
	if err != nil {
		t.Fatalf("ListByType rule: %v", err)
	}
	if len(rules) != 0 {
		t.Errorf("rule count = %d, want 0", len(rules))
	}
}

func TestListByPerson(t *testing.T) {
	store := newTestStore(t)

	store.Save(MemoryEntry{Type: MemoryPersonFact, Person: "Alice", Content: "a1", Confidence: 0.90, SourceSession: "ses_001", Tags: []string{}})
	store.Save(MemoryEntry{Type: MemoryPersonFact, Person: "Alice", Content: "a2", Confidence: 0.80, SourceSession: "ses_002", Tags: []string{}})
	store.Save(MemoryEntry{Type: MemoryPersonFact, Person: "Bob", Content: "b1", Confidence: 0.85, SourceSession: "ses_001", Tags: []string{}})

	aliceMemories, err := store.ListByPerson("Alice")
	if err != nil {
		t.Fatalf("ListByPerson Alice: %v", err)
	}
	if len(aliceMemories) != 2 {
		t.Errorf("Alice memories = %d, want 2", len(aliceMemories))
	}

	bobMemories, err := store.ListByPerson("Bob")
	if err != nil {
		t.Fatalf("ListByPerson Bob: %v", err)
	}
	if len(bobMemories) != 1 {
		t.Errorf("Bob memories = %d, want 1", len(bobMemories))
	}

	empty, err := store.ListByPerson("Nobody")
	if err != nil {
		t.Fatalf("ListByPerson Nobody: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("Nobody memories = %d, want 0", len(empty))
	}
}

func TestUpdateMemoryConfirmation(t *testing.T) {
	store := newTestStore(t)

	// Save a memory explicitly tagged as suggested (as the extractor would do for medium-confidence entries).
	entry := NewMemoryEntry(MemoryPersonFact, "test content", 0.75, "ses_001")
	entry.Suggested = true
	saved, err := store.Save(entry)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := store.Confirm(saved.ID); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	got, _ := store.Get(saved.ID)
	if !got.Confirmed {
		t.Error("Confirm should set Confirmed=true")
	}
	if got.Suggested {
		t.Error("Confirm should set Suggested=false")
	}
	if got.UpdatedAt <= saved.CreatedAt {
		t.Error("UpdatedAt should be newer after Confirm")
	}
}

func TestDeleteMemory(t *testing.T) {
	store := newTestStore(t)

	saved, err := store.Save(NewMemoryEntry(MemoryPersonFact, "test", 0.90, "ses_001"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Confirm it exists
	_, ok := store.Get(saved.ID)
	if !ok {
		t.Fatal("Get should return true before delete")
	}

	if err := store.Delete(saved.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, ok = store.Get(saved.ID)
	if ok {
		t.Error("Get should return false after delete")
	}
}

func TestDeleteNonexistentMemory(t *testing.T) {
	store := newTestStore(t)

	err := store.Delete("mem_nonexistent")
	if err == nil {
		t.Error("Delete nonexistent should return error")
	}
}

func TestUpdateMemoryConfidence(t *testing.T) {
	store := newTestStore(t)

	saved, err := store.Save(NewMemoryEntry(MemoryPersonFact, "decaying memory", 0.90, "ses_001"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := store.UpdateConfidence(saved.ID, 0.75); err != nil {
		t.Fatalf("UpdateConfidence: %v", err)
	}

	got, _ := store.Get(saved.ID)
	if got.Confidence != 0.75 {
		t.Errorf("Confidence = %f, want 0.75", got.Confidence)
	}
}

func TestListConfirmed(t *testing.T) {
	store := newTestStore(t)

	// Save entries and confirm some
	e1, _ := store.Save(NewMemoryEntry(MemoryPersonFact, "confirmed", 0.90, "ses_001"))
	e2Entry := NewMemoryEntry(MemoryUserTrait, "unconfirmed suggestion", 0.75, "ses_002")
	e2Entry.Suggested = true
	e2, _ := store.Save(e2Entry)
	_ = e1
	_ = e2

	store.Confirm(e1.ID)

	confirmed, err := store.ListConfirmed()
	if err != nil {
		t.Fatalf("ListConfirmed: %v", err)
	}
	if len(confirmed) != 1 {
		t.Errorf("ListConfirmed len = %d, want 1", len(confirmed))
	}

	// Check unconfirmed
	unconfirmed, err := store.ListUnconfirmed()
	if err != nil {
		t.Fatalf("ListUnconfirmed: %v", err)
	}
	if len(unconfirmed) != 1 {
		t.Errorf("ListUnconfirmed len = %d, want 1 (only unconfirmed suggestion)", len(unconfirmed))
	}
}

func TestFindSimilarByContent(t *testing.T) {
	store := newTestStore(t)

	store.Save(MemoryEntry{
		Type: MemoryPersonFact, Person: "Alice",
		Content: "Alice is a senior software engineer who loves Go", Confidence: 0.95, SourceSession: "ses_001", Tags: []string{},
	})

	similar, err := store.FindSimilarByContent("software engineer", 1)
	if err != nil {
		t.Fatalf("FindSimilarByContent: %v", err)
	}
	if len(similar) != 1 {
		t.Errorf("FindSimilarByContent len = %d, want 1", len(similar))
	}

	noMatch, err := store.FindSimilarByContent("zzz_nonexistent_zzz", 1)
	if err != nil {
		t.Fatalf("FindSimilarByContent no match: %v", err)
	}
	if len(noMatch) != 0 {
		t.Errorf("FindSimilarByContent no match len = %d, want 0", len(noMatch))
	}
}

func TestStoreSchemaExists(t *testing.T) {
	store := newTestStore(t)

	// Verify the memories table exists by inserting and querying
	entry := NewMemoryEntry(MemoryPersonFact, "schema test", 0.90, "ses_001")
	_, err := store.Save(entry)
	if err != nil {
		t.Fatalf("Save failed, schema may be missing: %v", err)
	}
}

// Regression: pagination total must stay accurate beyond the 1000-row cap
// that ListFiltered enforces. CountFiltered counts without the cap.
func TestCountFilteredIgnoresRowCap(t *testing.T) {
	store := newTestStore(t)
	for i := 0; i < 5; i++ {
		e := NewMemoryEntry(MemoryPersonFact, "fact about Alice", 0.9, "ses")
		e.Person = "Alice"
		if _, err := store.Save(e); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	n, err := store.CountFiltered(FilterOpts{Person: "Alice"})
	if err != nil {
		t.Fatalf("CountFiltered: %v", err)
	}
	if n != 5 {
		t.Errorf("count want 5, got %d", n)
	}
	// Filters compose the same way as ListFiltered.
	if n, _ := store.CountFiltered(FilterOpts{Person: "Bob"}); n != 0 {
		t.Errorf("count for unknown person want 0, got %d", n)
	}
	if n, _ := store.CountFiltered(FilterOpts{Type: string(MemoryPersonFact), HasMinConf: true, MinConf: 0.95}); n != 0 {
		t.Errorf("count above confidence want 0, got %d", n)
	}
}

// Regression: NaN confidence must be normalized, not written verbatim (NaN
// comparisons are always false, which would make the row undeletable by decay).
func TestUpdateConfidenceNormalizesNaN(t *testing.T) {
	store := newTestStore(t)
	e := NewMemoryEntry(MemoryPersonFact, "some fact", 0.9, "ses")
	saved, err := store.Save(e)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.UpdateConfidence(saved.ID, math.NaN()); err != nil {
		t.Fatalf("UpdateConfidence: %v", err)
	}
	got, ok := store.Get(saved.ID)
	if !ok {
		t.Fatal("Get failed")
	}
	if got.Confidence != 0 {
		t.Errorf("NaN confidence must normalize to 0, got %v", got.Confidence)
	}
}

// Regression: a disputed memory must carry the disputed flag — without it
// the UI's status logic (suggested? disputed? confirmed) fell through to
// "confirmed" right after the user marked it "inaccurate".
func TestDisputeSetsFlag(t *testing.T) {
	store := newTestStore(t)
	e := NewMemoryEntry(MemoryPersonFact, "some claim", 0.9, "ses")
	saved, err := store.Save(e)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Dispute(saved.ID); err != nil {
		t.Fatalf("Dispute: %v", err)
	}
	got, ok := store.Get(saved.ID)
	if !ok {
		t.Fatal("Get failed")
	}
	if !got.Disputed || got.Confirmed || got.Suggested {
		t.Errorf("disputed state wrong: %+v", got)
	}
	// Confirm clears disputed again.
	if err := store.Confirm(saved.ID); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	got, _ = store.Get(saved.ID)
	if got.Disputed || !got.Confirmed {
		t.Errorf("confirm must clear disputed: %+v", got)
	}
}
