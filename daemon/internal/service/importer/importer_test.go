package importer

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"openyou/daemon/internal/infra/config"
)

func setupTestDB(t *testing.T) (*sql.DB, *config.Store) {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	cStore, err := config.NewStore(db)
	if err != nil {
		t.Fatalf("config store: %v", err)
	}
	return db, cStore
}

func TestImport_HappyPath(t *testing.T) {
	_, cStore := setupTestDB(t)

	result, err := Import(cStore)
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	if result.Skipped {
		t.Error("first import should not be skipped")
	}
}

func TestImport_Idempotent(t *testing.T) {
	_, cStore := setupTestDB(t)

	result1, err := Import(cStore)
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if result1.Skipped {
		t.Fatal("first import should not skip")
	}

	result2, err := Import(cStore)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if !result2.Skipped {
		t.Error("second import should be skipped")
	}
}
