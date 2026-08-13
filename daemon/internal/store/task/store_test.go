package task

import (
	"database/sql"
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
	s, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestCreateListUpdateDelete(t *testing.T) {
	s := newTestStore(t)

	a, err := s.Create("write report", "2026-07-29", "2026-08-01", "user", "", 100)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if a.ID == "" || a.Done {
		t.Fatalf("unexpected task: %+v", a)
	}
	if a.Source != "user" {
		t.Errorf("expected source=user, got %q", a.Source)
	}
	if _, err := s.Create("undated todo", "", "", "", "", 200); err != nil {
		t.Fatalf("create 2: %v", err)
	}

	list, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 tasks, got %d", len(list))
	}
	// Dated task sorts before the undated one (both undone).
	if list[0].EndDate != "2026-08-01" {
		t.Errorf("expected dated task first, got %+v", list[0])
	}

	if err := s.Update(a.ID, "write final report", "", "2026-07-29", "2026-08-02", true, "high"); err != nil {
		t.Fatalf("update: %v", err)
	}
	list, _ = s.List()
	// Done task sorts last now.
	if !list[len(list)-1].Done {
		t.Errorf("done task should sort last, got %+v", list)
	}
	if list[len(list)-1].Priority != "high" {
		t.Errorf("priority not persisted, got %q", list[len(list)-1].Priority)
	}

	if err := s.Update("nope", "x", "", "", "", false, "normal"); err == nil {
		t.Error("update of missing id should error")
	}

	if err := s.Delete(a.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, _ = s.List()
	if len(list) != 1 {
		t.Errorf("want 1 after delete, got %d", len(list))
	}
}
