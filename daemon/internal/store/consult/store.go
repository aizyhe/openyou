// Package consult provides SQLite-backed session/message storage so the full
// history can be replayed to the LLM — openyou remembers earlier turns.
package consult

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"io"
	"log"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type FeedbackEntry struct {
	ID         string `json:"id"`
	SessionID  string `json:"session_id"`
	TurnIndex  int    `json:"turn_index"`
	Rating     string `json:"rating"`
	Correction string `json:"correction,omitempty"`
	CreatedAt  int64  `json:"created_at"`
}

type FeedbackPendingItem struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	Days      int    `json:"days"`
}

type Consult struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	CreatedAt int64      `json:"created_at"`
	UpdatedAt int64      `json:"updated_at"`
	Messages  []*Message `json:"messages,omitempty"`
}

type Message struct {
	ID        string `json:"id"`
	ConsultID string `json:"consult_id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt int64  `json:"created_at"`
}

const schema = `
CREATE TABLE IF NOT EXISTS consults (
    id         TEXT PRIMARY KEY,
    title      TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS messages (
    id         TEXT PRIMARY KEY,
    consult_id TEXT NOT NULL,
    role       TEXT NOT NULL,
    content    TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS feedback (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    turn_index INTEGER NOT NULL DEFAULT 0,
    rating TEXT NOT NULL DEFAULT '',
    correction TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_messages_consult ON messages(consult_id, created_at);
CREATE INDEX IF NOT EXISTS idx_consults_updated ON consults(updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_feedback_session ON feedback(session_id);
`

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("init session schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Create(title string) (*Consult, error) {
	now := time.Now().UnixNano()
	id, err := genID("ses")
	if err != nil {
		return nil, fmt.Errorf("create session: gen id: %w", err)
	}
	sess := &Consult{
		ID:        id,
		Title:     title,
		CreatedAt: now,
		UpdatedAt: now,
	}
	_, err = s.db.Exec(
		`INSERT INTO consults (id, title, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		sess.ID, sess.Title, sess.CreatedAt, sess.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return sess, nil
}

// ensure inserts a session row if missing, so client-generated consult_ids are durable.
func (s *Store) ensure(id, title string) error {
	now := time.Now().UnixNano()
	_, err := s.db.Exec(
		`INSERT INTO consults (id, title, created_at, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(id) DO NOTHING`,
		id, title, now, now,
	)
	if err != nil {
		return fmt.Errorf("ensure session %q: %w", id, err)
	}
	return nil
}

func (s *Store) EnsureWithTimestamp(id, title string, createdAt, updatedAt int64) error {
	_, err := s.db.Exec(
		`INSERT INTO consults (id, title, created_at, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(id) DO NOTHING`,
		id, title, createdAt, updatedAt,
	)
	if err != nil {
		return fmt.Errorf("ensure session %q: %w", id, err)
	}
	return nil
}

func (s *Store) List() ([]*Consult, error) {
	rows, err := s.db.Query(
		`SELECT id, title, created_at, updated_at FROM consults ORDER BY updated_at DESC LIMIT 200`,
	)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	result := make([]*Consult, 0)
	for rows.Next() {
		var s Consult
		if err := rows.Scan(&s.ID, &s.Title, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		result = append(result, &s)
	}
	return result, rows.Err()
}

func (s *Store) Get(id string) (*Consult, bool, error) {
	var sess Consult
	err := s.db.QueryRow(
		`SELECT id, title, created_at, updated_at FROM consults WHERE id = ?`, id,
	).Scan(&sess.ID, &sess.Title, &sess.CreatedAt, &sess.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get session %q: %w", id, err)
	}
	msgs, err := s.GetMessages(id)
	if err != nil {
		return nil, false, err
	}
	sess.Messages = msgs
	return &sess, true, nil
}

func (s *Store) Delete(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("delete session: begin tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM messages WHERE consult_id = ?`, id); err != nil {
		return fmt.Errorf("delete session messages %q: %w", id, err)
	}
	// Feedback rows reference the session — delete them too, or they'd orphan.
	if _, err := tx.Exec(`DELETE FROM feedback WHERE session_id = ?`, id); err != nil {
		return fmt.Errorf("delete session feedback %q: %w", id, err)
	}
	res, err := tx.Exec(`DELETE FROM consults WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete session %q: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("session %q not found", id)
	}
	return tx.Commit()
}

// AppendMessage persists one turn in a transaction, bumping updated_at so List
// stays ordered by recency; the first user message becomes the session title.
func (s *Store) AppendMessage(sessionID, role, content string) (*Message, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("append message: empty session id")
	}
	title := ""
	if role == "user" {
		title = truncateTitle(content)
	}

	now := time.Now().UnixNano()
	msgID, err := genID("msg")
	if err != nil {
		return nil, fmt.Errorf("append message: gen id: %w", err)
	}
	m := &Message{
		ID:        msgID,
		ConsultID: sessionID,
		Role:      role,
		Content:   content,
		CreatedAt: now,
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("append message: begin tx: %w", err)
	}
	defer tx.Rollback()

	// Ensure session row exists.
	if _, err := tx.Exec(
		`INSERT INTO consults (id, title, created_at, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(id) DO NOTHING`,
		sessionID, title, now, now,
	); err != nil {
		return nil, fmt.Errorf("ensure session %q: %w", sessionID, err)
	}

	// Insert the message.
	if _, err := tx.Exec(
		`INSERT INTO messages (id, consult_id, role, content, created_at) VALUES (?, ?, ?, ?, ?)`,
		m.ID, m.ConsultID, m.Role, m.Content, m.CreatedAt,
	); err != nil {
		return nil, fmt.Errorf("append message: %w", err)
	}

	// Bump updated_at (and set the title on first user message). A metadata UPDATE
	// failure is logged but doesn't abort — the message itself already inserted.
	if role == "user" {
		if _, err := tx.Exec(
			`UPDATE consults SET updated_at = ?, title = CASE WHEN title = '' THEN ? ELSE title END WHERE id = ?`,
			now, title, sessionID,
		); err != nil {
			log.Printf("session: update session %q metadata: %v", sessionID, err)
		}
	} else {
		if _, err := tx.Exec(`UPDATE consults SET updated_at = ? WHERE id = ?`, now, sessionID); err != nil {
			log.Printf("session: update session %q updated_at: %v", sessionID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("append message: commit: %w", err)
	}
	return m, nil
}

func (s *Store) ListWithMessages() ([]*Consult, error) {
	rows, err := s.db.Query(
		`SELECT s.id, s.title, s.created_at, s.updated_at,
		        m.id, m.consult_id, m.role, m.content, m.created_at
		 FROM consults s
		 LEFT JOIN messages m ON m.consult_id = s.id
		 ORDER BY s.updated_at DESC, m.created_at ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list sessions with messages: %w", err)
	}
	defer rows.Close()

	var ordered []string
	sessMap := make(map[string]*Consult)

	for rows.Next() {
		var (
			sID, sTitle                string
			sCreatedAt, sUpdAt         int64
			mID, mSID, mRole, mContent sql.NullString
			mCreatedAt                 sql.NullInt64
		)
		if err := rows.Scan(&sID, &sTitle, &sCreatedAt, &sUpdAt,
			&mID, &mSID, &mRole, &mContent, &mCreatedAt); err != nil {
			return nil, fmt.Errorf("scan session+message row: %w", err)
		}
		sess, exists := sessMap[sID]
		if !exists {
			sess = &Consult{
				ID: sID, Title: sTitle,
				CreatedAt: sCreatedAt, UpdatedAt: sUpdAt,
				Messages: []*Message{},
			}
			sessMap[sID] = sess
			ordered = append(ordered, sID)
		}
		if mID.Valid {
			sess.Messages = append(sess.Messages, &Message{
				ID:        mID.String,
				ConsultID: mSID.String,
				Role:      mRole.String,
				Content:   mContent.String,
				CreatedAt: mCreatedAt.Int64,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sessions with messages rows: %w", err)
	}

	result := make([]*Consult, 0, len(ordered))
	for _, id := range ordered {
		result = append(result, sessMap[id])
	}
	return result, nil
}
func (s *Store) GetMessages(sessionID string) ([]*Message, error) {
	rows, err := s.db.Query(
		`SELECT id, consult_id, role, content, created_at
		 FROM messages WHERE consult_id = ? ORDER BY created_at ASC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("get messages %q: %w", sessionID, err)
	}
	defer rows.Close()

	result := make([]*Message, 0)
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ConsultID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		result = append(result, &m)
	}
	return result, rows.Err()
}

func truncateTitle(s string) string {
	const max = 40
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

func (s *Store) SaveFeedback(e *FeedbackEntry) error {
	if e.ID == "" {
		e.ID = fmt.Sprintf("%s-%d", e.SessionID, e.TurnIndex)
	}
	if e.CreatedAt == 0 {
		e.CreatedAt = time.Now().UnixNano()
	}
	_, err := s.db.Exec(
		`INSERT INTO feedback (id, session_id, turn_index, rating, correction, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET rating=excluded.rating, correction=excluded.correction`,
		e.ID, e.SessionID, e.TurnIndex, e.Rating, e.Correction, e.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("save feedback: %w", err)
	}
	return nil
}

// RestoreFeedback is insert-only (no overwrite), so a locally-changed rating keeps winning.
func (s *Store) RestoreFeedback(e *FeedbackEntry) error {
	if e.ID == "" {
		e.ID = fmt.Sprintf("%s-%d", e.SessionID, e.TurnIndex)
	}
	if e.CreatedAt == 0 {
		e.CreatedAt = time.Now().UnixNano()
	}
	_, err := s.db.Exec(
		`INSERT INTO feedback (id, session_id, turn_index, rating, correction, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO NOTHING`,
		e.ID, e.SessionID, e.TurnIndex, e.Rating, e.Correction, e.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("restore feedback: %w", err)
	}
	return nil
}

func (s *Store) ListFeedbackBySession(sessionID string) ([]*FeedbackEntry, error) {
	rows, err := s.db.Query(
		"SELECT id, session_id, turn_index, rating, correction, created_at FROM feedback WHERE session_id=? ORDER BY turn_index",
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []*FeedbackEntry
	for rows.Next() {
		var e FeedbackEntry
		if err := rows.Scan(&e.ID, &e.SessionID, &e.TurnIndex, &e.Rating, &e.Correction, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, &e)
	}
	return entries, rows.Err()
}

func (s *Store) HasFeedback(sessionID string) (bool, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(1) FROM feedback WHERE session_id=?", sessionID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("has feedback: %w", err)
	}
	return n > 0, nil
}

func (s *Store) RecentUsefulFeedback(limit int) ([]*FeedbackEntry, error) {
	rows, err := s.db.Query(
		"SELECT id, session_id, turn_index, rating, correction, created_at FROM feedback WHERE rating='useful' ORDER BY created_at DESC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []*FeedbackEntry
	for rows.Next() {
		var e FeedbackEntry
		if err := rows.Scan(&e.ID, &e.SessionID, &e.TurnIndex, &e.Rating, &e.Correction, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, &e)
	}
	return entries, rows.Err()
}

func genID(prefix string) (string, error) {
	b := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s_%x", prefix, b), nil
}

// ListAllFeedback returns every feedback entry (backup export) — unbounded on purpose.
func (s *Store) ListAllFeedback() ([]*FeedbackEntry, error) {
	rows, err := s.db.Query(
		"SELECT id, session_id, turn_index, rating, correction, created_at FROM feedback ORDER BY created_at",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []*FeedbackEntry
	for rows.Next() {
		var e FeedbackEntry
		if err := rows.Scan(&e.ID, &e.SessionID, &e.TurnIndex, &e.Rating, &e.Correction, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, &e)
	}
	return entries, rows.Err()
}
