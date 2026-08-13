package db

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMigrate_AppliesOnce(t *testing.T) {
	db := openTestDB(t)
	migrations := []Migration{
		{Version: 1, Up: `CREATE TABLE t (id INTEGER PRIMARY KEY)`},
		{Version: 2, Up: `ALTER TABLE t ADD COLUMN name TEXT NOT NULL DEFAULT ''`},
	}
	// First run: both applied
	if err := Migrate(db, migrations); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	// Second run: idempotent, no error
	if err := Migrate(db, migrations); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	// Table and column exist
	if ok, err := HasColumn(db, "t", "name"); err != nil || !ok {
		t.Errorf("expected column 'name' after migration: ok=%v err=%v", ok, err)
	}
}

func TestAddColumnIfMissing(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(`CREATE TABLE foo (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	// Add new column — should succeed
	if err := AddColumnIfMissing(db, "foo", "score", `score REAL NOT NULL DEFAULT 0`); err != nil {
		t.Fatalf("AddColumnIfMissing first call: %v", err)
	}
	// Call again — column exists, must be a no-op (not an error)
	if err := AddColumnIfMissing(db, "foo", "score", `score REAL NOT NULL DEFAULT 0`); err != nil {
		t.Fatalf("AddColumnIfMissing second call (should be no-op): %v", err)
	}
	if ok, _ := HasColumn(db, "foo", "score"); !ok {
		t.Error("column 'score' should exist")
	}
}

func TestHasColumn_MissingTable(t *testing.T) {
	db := openTestDB(t)
	// Table doesn't exist — PRAGMA returns empty result, not an error
	ok, err := HasColumn(db, "nonexistent", "col")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected false for nonexistent table")
	}
}
