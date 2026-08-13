package consult

import (
	"database/sql"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func newTestStoreDB(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// Pin to a single underlying connection so raw inserts and Store queries
	// share the same in-memory database.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	s, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s, db
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, _ := newTestStoreDB(t)
	return s
}

func TestNewStore_SchemaError(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.Close() // closed DB → Exec fails
	if _, err := NewStore(db); err == nil {
		t.Fatal("expected error initializing schema on closed DB")
	}
}

func TestCreateAndGet(t *testing.T) {
	s := newTestStore(t)

	sess, err := s.Create("Hello")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.ID == "" || !strings.HasPrefix(sess.ID, "ses_") {
		t.Errorf("bad id %q", sess.ID)
	}
	if sess.Title != "Hello" {
		t.Errorf("title = %q", sess.Title)
	}
	if sess.CreatedAt == 0 || sess.UpdatedAt == 0 {
		t.Error("timestamps not set")
	}

	got, ok, err := s.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected session to exist")
	}
	if got.ID != sess.ID || got.Title != "Hello" {
		t.Errorf("got %+v", got)
	}
	if len(got.Messages) != 0 {
		t.Errorf("expected 0 messages, got %d", len(got.Messages))
	}
}

func TestGet_NotFound(t *testing.T) {
	s := newTestStore(t)
	got, ok, err := s.Get("nope")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok || got != nil {
		t.Errorf("expected not found, got ok=%v sess=%v", ok, got)
	}
}

func TestList(t *testing.T) {
	s := newTestStore(t)

	// Empty list.
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d", len(list))
	}

	a, _ := s.Create("first")
	b, _ := s.Create("second")

	// Bump a so it becomes most-recently-updated.
	if _, err := s.AppendMessage(a.ID, "user", "hi"); err != nil {
		t.Fatal(err)
	}

	list, err = s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(list))
	}
	// a was updated most recently, should be first.
	if list[0].ID != a.ID {
		t.Errorf("expected %s first (most recent), got %s", a.ID, list[0].ID)
	}
	if list[1].ID != b.ID {
		t.Errorf("expected %s second, got %s", b.ID, list[1].ID)
	}
	// List returns no messages.
	if len(list[0].Messages) != 0 {
		t.Error("List should not include messages")
	}
}

func TestAppendMessage_EmptySessionID(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.AppendMessage("", "user", "hi"); err == nil {
		t.Fatal("expected error on empty session id")
	}
}

func TestAppendMessage_CreatesSessionAndSetsTitle(t *testing.T) {
	s := newTestStore(t)

	// AppendMessage on a fresh client-generated id ensures the session row.
	m, err := s.AppendMessage("ses_client1", "user", "What is my name?")
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if m.ID == "" || !strings.HasPrefix(m.ID, "msg_") {
		t.Errorf("bad message id %q", m.ID)
	}
	if m.Role != "user" || m.Content != "What is my name?" {
		t.Errorf("bad message %+v", m)
	}

	sess, ok, err := s.Get("ses_client1")
	if err != nil || !ok {
		t.Fatalf("Get after append: ok=%v err=%v", ok, err)
	}
	if sess.Title != "What is my name?" {
		t.Errorf("title should be set from first user message, got %q", sess.Title)
	}
	if len(sess.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(sess.Messages))
	}
}

func TestAppendMessage_TitleNotOverwritten(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.AppendMessage("ses_x", "user", "first user msg"); err != nil {
		t.Fatal(err)
	}
	// Assistant message: title path not taken.
	if _, err := s.AppendMessage("ses_x", "assistant", "reply"); err != nil {
		t.Fatal(err)
	}
	// Second user message: title already set, must not overwrite.
	if _, err := s.AppendMessage("ses_x", "user", "second user msg"); err != nil {
		t.Fatal(err)
	}

	sess, ok, _ := s.Get("ses_x")
	if !ok {
		t.Fatal("expected session")
	}
	if sess.Title != "first user msg" {
		t.Errorf("title should remain first user message, got %q", sess.Title)
	}
	if len(sess.Messages) != 3 {
		t.Errorf("expected 3 messages, got %d", len(sess.Messages))
	}
	// Chronological order.
	if sess.Messages[0].Content != "first user msg" ||
		sess.Messages[1].Content != "reply" ||
		sess.Messages[2].Content != "second user msg" {
		t.Errorf("messages out of order: %+v", sess.Messages)
	}
}

func TestGetMessages_Empty(t *testing.T) {
	s := newTestStore(t)
	msgs, err := s.GetMessages("no-session")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)

	sess, _ := s.Create("to delete")
	if _, err := s.AppendMessage(sess.ID, "user", "hi"); err != nil {
		t.Fatal(err)
	}

	if err := s.Delete(sess.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, ok, err := s.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("session should be gone after Delete")
	}
	msgs, _ := s.GetMessages(sess.ID)
	if len(msgs) != 0 {
		t.Errorf("messages should be deleted, got %d", len(msgs))
	}

	// DB-A8: deleting a non-existent session now returns an error.
	if err := s.Delete("nonexistent"); err == nil {
		t.Error("Delete nonexistent should error")
	}
}

func TestTruncateTitle(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"short", "hello", "hello"},
		{"empty", "", ""},
		{"exactly max", strings.Repeat("a", 40), strings.Repeat("a", 40)},
		{"over max", strings.Repeat("b", 41), strings.Repeat("b", 40) + "…"},
		{"unicode over max", strings.Repeat("你", 41), strings.Repeat("你", 40) + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateTitle(tt.in); got != tt.want {
				t.Errorf("truncateTitle(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestAppendMessage_LongTitleTruncated(t *testing.T) {
	s := newTestStore(t)
	long := strings.Repeat("x", 100)
	if _, err := s.AppendMessage("ses_long", "user", long); err != nil {
		t.Fatal(err)
	}
	sess, ok, _ := s.Get("ses_long")
	if !ok {
		t.Fatal("expected session")
	}
	if !strings.HasSuffix(sess.Title, "…") {
		t.Errorf("long title should be truncated with ellipsis, got %q", sess.Title)
	}
}

func TestGenID(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id, err := genID("pfx")
		if err != nil {
			t.Fatalf("genID error: %v", err)
		}
		if !strings.HasPrefix(id, "pfx_") {
			t.Errorf("bad prefix: %q", id)
		}
		if seen[id] {
			t.Errorf("duplicate id: %q", id)
		}
		seen[id] = true
	}
}

// Scan-error paths: a non-numeric created_at cannot scan into int64, producing
// a non-ErrNoRows error that exercises List/Get/GetMessages scan branches.
func TestScanErrors(t *testing.T) {
	s, db := newTestStoreDB(t)

	if _, err := db.Exec(
		`INSERT INTO consults (id, title, created_at, updated_at) VALUES ('bad', 't', 'not-a-number', 1)`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(); err == nil {
		t.Error("List should error scanning non-numeric created_at")
	}
	if _, _, err := s.Get("bad"); err == nil {
		t.Error("Get should error scanning non-numeric created_at")
	}

	if _, err := db.Exec(
		`INSERT INTO messages (id, consult_id, role, content, created_at) VALUES ('m1', 'sX', 'user', 'c', 'nope')`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetMessages("sX"); err == nil {
		t.Error("GetMessages should error scanning non-numeric created_at")
	}

	// Get on a valid session whose messages fail to scan exercises Get's
	// GetMessages-error branch.
	if _, err := db.Exec(
		`INSERT INTO consults (id, title, created_at, updated_at) VALUES ('good', 't', 1, 2)`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO messages (id, consult_id, role, content, created_at) VALUES ('m2', 'good', 'user', 'c', 'nope')`,
	); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get("good"); err == nil {
		t.Error("Get should propagate GetMessages scan error")
	}
}

// Delete's second Exec (sessions) error branch: drop the sessions table but
// keep messages so the first Exec succeeds and the second fails.
func TestDelete_SessionsTableMissing(t *testing.T) {
	s, db := newTestStoreDB(t)
	if _, err := db.Exec(`DROP TABLE consults`); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("anything"); err == nil {
		t.Error("Delete should error when consults table is missing")
	}
}

// AppendMessage insert-error branch: ensure() (into consults) succeeds but the
// messages insert fails because the messages table is gone.
func TestAppendMessage_MessagesTableMissing(t *testing.T) {
	s, db := newTestStoreDB(t)
	if _, err := db.Exec(`DROP TABLE messages`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage("ses_y", "user", "hi"); err == nil {
		t.Error("AppendMessage should error when messages table is missing")
	}
}

// Error-path coverage: operations on a closed DB should surface errors.
func TestStore_ClosedDBErrors(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	if _, err := s.Create("x"); err == nil {
		t.Error("Create on closed DB should error")
	}
	if _, err := s.List(); err == nil {
		t.Error("List on closed DB should error")
	}
	if _, _, err := s.Get("x"); err == nil {
		t.Error("Get on closed DB should error")
	}
	if _, err := s.GetMessages("x"); err == nil {
		t.Error("GetMessages on closed DB should error")
	}
	if err := s.Delete("x"); err == nil {
		t.Error("Delete on closed DB should error")
	}
	if _, err := s.AppendMessage("x", "user", "y"); err == nil {
		t.Error("AppendMessage on closed DB should error (ensure fails)")
	}
}
