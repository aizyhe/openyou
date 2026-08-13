// Package task provides SQLite-backed storage for user tasks with a time window.
// Calendar events are tasks viewed by date; Star suggestions may be accepted as tasks.
package task

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"time"
)

type Task struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	StartDate   string `json:"start_date"`
	EndDate     string `json:"end_date"`
	Done        bool   `json:"done"`
	Priority    string `json:"priority"`
	Source      string `json:"source"`
	SessionID   string `json:"session_id"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

const schema = `
CREATE TABLE IF NOT EXISTS time_task (
    id          TEXT PRIMARY KEY,
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    start_date  TEXT NOT NULL DEFAULT '',
    end_date    TEXT NOT NULL DEFAULT '',
    done        INTEGER NOT NULL DEFAULT 0,
    priority    TEXT NOT NULL DEFAULT 'normal',
    source      TEXT NOT NULL DEFAULT 'user',
    session_id  TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_time_task_end ON time_task(end_date);
CREATE INDEX IF NOT EXISTS idx_time_task_done ON time_task(done, end_date);
`

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("init task schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) List() ([]*Task, error) {
	rows, err := s.db.Query(
		`SELECT id, title, description, start_date, end_date, done, priority, source, session_id, created_at, updated_at FROM time_task
		 ORDER BY done ASC,
		          CASE WHEN end_date = '' THEN 1 ELSE 0 END ASC,
		          end_date ASC,
		          created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	result := make([]*Task, 0)
	for rows.Next() {
		var t Task
		var done int
		if err := rows.Scan(&t.ID, &t.Title, &t.Description, &t.StartDate, &t.EndDate, &done, &t.Priority, &t.Source, &t.SessionID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		t.Done = done != 0
		result = append(result, &t)
	}
	return result, rows.Err()
}

func (s *Store) Create(title, startDate, endDate, source, sessionID string, createdAt int64) (*Task, error) {
	now := time.Now().UnixNano()
	if source == "" {
		source = "user"
	}
	id, err := genID("tsk")
	if err != nil {
		return nil, fmt.Errorf("create task: gen id: %w", err)
	}
	t := &Task{
		ID:        id,
		Title:     title,
		StartDate: startDate,
		EndDate:   endDate,
		Done:      false,
		Priority:  "normal",
		Source:    source,
		SessionID: sessionID,
		CreatedAt: createdAt,
		UpdatedAt: now,
	}
	_, err = s.db.Exec(
		`INSERT INTO time_task (id, title, description, start_date, end_date, done, priority, source, session_id, created_at, updated_at) VALUES (?, ?, '', ?, ?, 0, 'normal', ?, ?, ?, ?)`,
		t.ID, t.Title, t.StartDate, t.EndDate, t.Source, t.SessionID, t.CreatedAt, t.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}
	return t, nil
}

// CreateFull inserts a task with description and priority (Do-suggestion, UI form).
func (s *Store) CreateFull(title, description, startDate, endDate, priority, source, sessionID string, createdAt int64) (*Task, error) {
	now := time.Now().UnixNano()
	if source == "" {
		source = "user"
	}
	if priority == "" {
		priority = "normal"
	}
	id, err := genID("tsk")
	if err != nil {
		return nil, fmt.Errorf("create task: gen id: %w", err)
	}
	t := &Task{
		ID:          id,
		Title:       title,
		Description: description,
		StartDate:   startDate,
		EndDate:     endDate,
		Done:        false,
		Priority:    priority,
		Source:      source,
		SessionID:   sessionID,
		CreatedAt:   createdAt,
		UpdatedAt:   now,
	}
	_, err = s.db.Exec(
		`INSERT INTO time_task (id, title, description, start_date, end_date, done, priority, source, session_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?)`,
		t.ID, t.Title, t.Description, t.StartDate, t.EndDate, t.Priority, t.Source, t.SessionID, t.CreatedAt, t.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}
	return t, nil
}

func (s *Store) Get(id string) (*Task, error) {
	var t Task
	var done int
	err := s.db.QueryRow(
		`SELECT id, title, description, start_date, end_date, done, priority, source, session_id, created_at, updated_at FROM time_task WHERE id = ?`, id,
	).Scan(&t.ID, &t.Title, &t.Description, &t.StartDate, &t.EndDate, &done, &t.Priority, &t.Source, &t.SessionID, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("task %q not found", id)
		}
		return nil, fmt.Errorf("get task %q: %w", id, err)
	}
	t.Done = done != 0
	return &t, nil
}

func (s *Store) Update(id, title, description, startDate, endDate string, done bool, priority string) error {
	doneInt := 0
	if done {
		doneInt = 1
	}
	if priority == "" {
		priority = "normal"
	}
	res, err := s.db.Exec(
		`UPDATE time_task SET title = ?, description = ?, start_date = ?, end_date = ?, done = ?, priority = ?, updated_at = ? WHERE id = ?`,
		title, description, startDate, endDate, doneInt, priority, time.Now().UnixNano(), id,
	)
	if err != nil {
		return fmt.Errorf("update task %q: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("task %q not found", id)
	}
	return nil
}

func (s *Store) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM time_task WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete task %q: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("task %q not found", id)
	}
	return nil
}

// Upsert is INSERT OR REPLACE for backup restore, generating an ID if empty.
func (s *Store) Upsert(t *Task) error {
	if t.ID == "" {
		id, err := genID("tsk")
		if err != nil {
			return fmt.Errorf("upsert task: gen id: %w", err)
		}
		t.ID = id
	}
	doneInt := 0
	if t.Done {
		doneInt = 1
	}
	if t.Priority == "" {
		t.Priority = "normal"
	}
	if t.UpdatedAt == 0 {
		t.UpdatedAt = time.Now().UnixNano()
	}
	if t.Source == "" {
		t.Source = "user"
	}
	// Backup restore always provides all fields, so drop-and-reinsert is safe here.
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO time_task (id, title, description, start_date, end_date, done, priority, source, session_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Title, t.Description, t.StartDate, t.EndDate, doneInt, t.Priority, t.Source, t.SessionID, t.CreatedAt, t.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert task %q: %w", t.ID, err)
	}
	return nil
}

func genID(prefix string) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("genID: %w", err)
	}
	return fmt.Sprintf("%s_%x", prefix, b), nil
}
