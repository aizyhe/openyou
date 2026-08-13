package memory

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// stubLLM is a controllable LLMClient for extractor tests.
type stubLLM struct {
	resp string
	err  error
}

func (s *stubLLM) Chat(_ context.Context, _, _ string) (string, error) {
	return s.resp, s.err
}

// longConvo returns a conversation with >=50 fields so Extract doesn't short-circuit.
func longConvo() string {
	out := ""
	for i := 0; i < 60; i++ {
		out += "word "
	}
	return out
}

func TestLLMExtractor_NilClient(t *testing.T) {
	e := NewLLMMemoryExtractor(context.Background(), nil)
	got, err := e.Extract("anything")
	if err != nil || got != nil {
		t.Errorf("nil client should yield (nil,nil), got %v %v", got, err)
	}
}

func TestLLMExtractor_ShortConversationSkipped(t *testing.T) {
	e := NewLLMMemoryExtractor(context.Background(), &stubLLM{resp: "[]"})
	got, err := e.Extract("too short")
	if err != nil || got != nil {
		t.Errorf("short convo should be skipped, got %v %v", got, err)
	}
}

func TestLLMExtractor_Success(t *testing.T) {
	resp := "```json\n" +
		`[{"type":"person_fact","person":"Alice","content":"Alice likes tea","confidence":0.9},` +
		`{"type":"context","person":"","content":"low conf","confidence":0.3},` +
		`{"type":"rule","person":"","content":"","confidence":0.9}]` +
		"\n```"
	e := NewLLMMemoryExtractor(context.Background(), &stubLLM{resp: resp})
	got, err := e.Extract(longConvo())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	// Only the high-confidence, non-empty entry survives the parse filter.
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d: %+v", len(got), got)
	}
	if got[0].Person != "Alice" || got[0].Type != MemoryPersonFact {
		t.Errorf("wrong entry: %+v", got[0])
	}
}

func TestLLMExtractor_ChatError(t *testing.T) {
	e := NewLLMMemoryExtractor(context.Background(), &stubLLM{err: errors.New("boom")})
	if _, err := e.Extract(longConvo()); err == nil {
		t.Error("expected error from failed Chat")
	}
}

func TestLLMExtractor_BadJSON(t *testing.T) {
	e := NewLLMMemoryExtractor(context.Background(), &stubLLM{resp: "not json"})
	if _, err := e.Extract(longConvo()); err == nil {
		t.Error("expected parse error on bad JSON")
	}
}

// TestDispute covers both the success (halve confidence) and not-found branches.
func TestDispute(t *testing.T) {
	store := newTestStore(t)
	saved, err := store.Save(MemoryEntry{Type: MemoryPersonFact, Person: "A", Content: "x", Confidence: 0.8, SourceSession: "s", Confirmed: true, Suggested: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Dispute(saved.ID); err != nil {
		t.Fatalf("Dispute: %v", err)
	}
	got, ok := store.Get(saved.ID)
	if !ok {
		t.Fatal("gone after dispute")
	}
	if got.Confidence >= 0.8 || got.Confirmed || got.Suggested {
		t.Errorf("dispute should halve confidence and clear flags, got %+v", got)
	}
	if err := store.Dispute("nonexistent"); err == nil {
		t.Error("expected not-found error disputing missing memory")
	}
}

// TestUpdateConfidenceAndConfirmedLists exercises UpdateConfidence + ListConfirmed.
func TestUpdateConfidenceAndConfirmedLists(t *testing.T) {
	store := newTestStore(t)
	saved, _ := store.Save(NewMemoryEntry(MemoryPersonFact, "c", 0.5, "s"))
	if err := store.UpdateConfidence(saved.ID, 0.99); err != nil {
		t.Fatalf("UpdateConfidence: %v", err)
	}
	if err := store.Confirm(saved.ID); err != nil {
		t.Fatal(err)
	}
	conf, err := store.ListConfirmed()
	if err != nil {
		t.Fatal(err)
	}
	if len(conf) != 1 {
		t.Errorf("ListConfirmed len = %d, want 1", len(conf))
	}
}

// TestFindSimilarByContentEscapes ensures LIKE metacharacters are literal.
func TestFindSimilarByContentEscapes(t *testing.T) {
	store := newTestStore(t)
	store.Save(MemoryEntry{Type: MemoryContext, Content: "80% done", Confidence: 0.9, SourceSession: "s", Tags: []string{}})
	store.Save(MemoryEntry{Type: MemoryContext, Content: "totally done", Confidence: 0.9, SourceSession: "s", Tags: []string{}})
	got, err := store.FindSimilarByContent("80%", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != "80% done" {
		t.Errorf("literal %% search wrong: %+v", got)
	}
}

// TestStoreListErrorsOnClosedDB covers the query-error branches of the list
// methods and Save/Delete/Confirm error paths.
func TestStoreListErrorsOnClosedDB(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	if _, err := store.ListAll(); err == nil {
		t.Error("ListAll should error on closed DB")
	}
	if _, err := store.ListByType(MemoryPersonFact); err == nil {
		t.Error("ListByType should error on closed DB")
	}
	if _, err := store.ListByPerson("A"); err == nil {
		t.Error("ListByPerson should error on closed DB")
	}
	if _, err := store.ListConfirmed(); err == nil {
		t.Error("ListConfirmed should error on closed DB")
	}
	if _, err := store.ListUnconfirmed(); err == nil {
		t.Error("ListUnconfirmed should error on closed DB")
	}
	if _, err := store.FindSimilarByContent("x", 5); err == nil {
		t.Error("FindSimilarByContent should error on closed DB")
	}
	if _, err := store.Save(NewMemoryEntry(MemoryRule, "x", 0.9, "s")); err == nil {
		t.Error("Save should error on closed DB")
	}
	if err := store.Confirm("x"); err == nil {
		t.Error("Confirm should error on closed DB")
	}
	if err := store.UpdateConfidence("x", 0.1); err == nil {
		t.Error("UpdateConfidence should error on closed DB")
	}
	if err := store.Dispute("x"); err == nil {
		t.Error("Dispute should error on closed DB")
	}
	if err := store.Delete("x"); err == nil {
		t.Error("Delete should error on closed DB")
	}
}

func TestNewStoreError(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := NewStore(db); err == nil {
		t.Error("expected NewStore error on closed DB")
	}
}

// TestFindConflictsSkipBranches covers the early-continue branches (empty
// person, mismatched person, mismatched type) plus a real conflict.
func TestFindConflictsSkipBranches(t *testing.T) {
	newEntries := []MemoryEntry{
		{Person: "", Type: MemoryPersonFact, Content: "loves coffee"},      // empty person → skip
		{Person: "Alice", Type: MemoryPersonFact, Content: "hates coffee"}, // real conflict
	}
	existing := []MemoryEntry{
		{Person: "", Type: MemoryPersonFact, Content: "loves coffee"},      // existing empty → skip
		{Person: "Bob", Type: MemoryPersonFact, Content: "loves coffee"},   // different person → skip
		{Person: "Alice", Type: MemoryUserTrait, Content: "loves coffee"},  // different type → skip
		{Person: "Alice", Type: MemoryPersonFact, Content: "loves coffee"}, // opposing polarity → conflict
	}
	conflicts := FindConflicts(newEntries, existing)
	if len(conflicts) != 1 {
		t.Fatalf("expected exactly 1 conflict, got %d: %+v", len(conflicts), conflicts)
	}
	if conflicts[0].New.Person != "Alice" {
		t.Errorf("wrong conflict entry: %+v", conflicts[0])
	}
}

// Pins the A4 fix: recall must match long composite queries (brain-trust
// history+question), not require the whole query as one substring.
func TestFindSimilarByContentTokenRecall(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(NewMemoryEntry(MemoryPersonFact, "张伟喜欢喝美式咖啡", 0.9, "s1")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(NewMemoryEntry(MemoryPersonFact, "李明负责后端平台", 0.8, "s2")); err != nil {
		t.Fatal(err)
	}

	// A contextual query carrying history + the current question. Under the old
	// whole-substring LIKE this matched NOTHING.
	query := "对话历史（供你记住上下文）:\n用户: 我之前说过张伟的爱好\n你: 好的\n\n当前问题: 张伟喜欢喝什么咖啡？"
	got, err := store.FindSimilarByContent(query, 5)
	if err != nil {
		t.Fatalf("FindSimilarByContent: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("token recall matched nothing; brain-trust memory recall is dead")
	}
	if !strings.Contains(got[0].Content, "张伟") {
		t.Errorf("best match should be the Zhang Wei memory, got %q", got[0].Content)
	}

	// English/ASCII words also recall.
	if _, err := store.Save(NewMemoryEntry(MemoryUserTrait, "prefers dark mode in editors", 0.7, "s3")); err != nil {
		t.Fatal(err)
	}
	got2, err := store.FindSimilarByContent("history blah blah; question: does he like dark mode?", 5)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range got2 {
		if strings.Contains(e.Content, "dark mode") {
			found = true
		}
	}
	if !found {
		t.Errorf("ASCII word recall failed: %+v", got2)
	}

	// Empty-token query returns nothing, no error.
	got3, err := store.FindSimilarByContent("?", 5)
	if err != nil || len(got3) != 0 {
		t.Errorf("no-token query want (nil,nil), got %v err %v", got3, err)
	}
}
