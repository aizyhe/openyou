package db

import (
	"database/sql"
	"fmt"
	"log"
	"regexp"
	"time"
)

type Migration struct {
	Version int
	Up      string
}

// colDefRe whitelists colDef passed to AddColumnIfMissing against SQL injection.
var colDefRe = regexp.MustCompile(
	`^[a-zA-Z_][a-zA-Z0-9_]*(\s+(TEXT|INTEGER|REAL|BLOB))?(\s+NOT\s+NULL)?(\s+DEFAULT\s+([0-9]+|'[^']*'))?$`,
)

func Migrate(db *sql.DB, migrations []Migration) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	for _, m := range migrations {
		var count int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.Version,
		).Scan(&count); err != nil {
			return fmt.Errorf("migration v%d: check applied: %w", m.Version, err)
		}
		if count > 0 {
			continue // already applied
		}
		// Migration SQL and the version insert run in one transaction so
		// a crash leaves no orphaned half-state; defer scoped to a single apply.
		if err := applyOne(db, m); err != nil {
			return err
		}
		log.Printf("db: applied migration v%d", m.Version)
	}
	return nil
}

func applyOne(db *sql.DB, m Migration) error {
	tx, txErr := db.Begin()
	if txErr != nil {
		return fmt.Errorf("migration v%d: begin tx: %w", m.Version, txErr)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(m.Up); err != nil {
		return fmt.Errorf("migration v%d: %w", m.Version, err)
	}
	// Use UnixNano() for consistency with all other timestamp columns.
	if _, err := tx.Exec(
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		m.Version, time.Now().UnixNano(),
	); err != nil {
		return fmt.Errorf("migration v%d: record applied: %w", m.Version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migration v%d: commit: %w", m.Version, err)
	}
	return nil
}

// HasColumn guards ALTER TABLE ADD COLUMN in migrations — SQLite has no
// ADD COLUMN IF NOT EXISTS.
func HasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%q)`, table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// AddColumnIfMissing adds a column only when absent (SQLite has no IF NOT
// EXISTS); colDef is whitelisted to prevent SQL injection.
func AddColumnIfMissing(db *sql.DB, table, column, colDef string) error {
	if !colDefRe.MatchString(colDef) {
		return fmt.Errorf("invalid column definition: %q", colDef)
	}
	exists, err := HasColumn(db, table, column)
	if err != nil {
		return fmt.Errorf("check column %s.%s: %w", table, column, err)
	}
	if exists {
		return nil
	}
	_, err = db.Exec(fmt.Sprintf(`ALTER TABLE %q ADD COLUMN %s`, table, colDef))
	if err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
}
