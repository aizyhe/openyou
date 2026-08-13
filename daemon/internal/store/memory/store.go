package memory

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	_ "github.com/mattn/go-sqlite3"

	dbpkg "openyou/daemon/internal/infra/db"
)

type Store struct {
	db *sql.DB
	// markdownMu serializes writeMarkdown so concurrent writes to the same month
	// file don't interleave read-render-write-rename and drop each other's changes.
	markdownMu sync.Mutex
}

const schema = `
CREATE TABLE IF NOT EXISTS context_memory (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    person TEXT DEFAULT '',
    content TEXT NOT NULL,
    confidence REAL NOT NULL DEFAULT 0.0,
    source_session TEXT NOT NULL,
    confirmed INTEGER NOT NULL DEFAULT 0,
    suggested INTEGER NOT NULL DEFAULT 0,
    disputed INTEGER NOT NULL DEFAULT 0,
    decay_rate REAL NOT NULL DEFAULT 0.01,
    relevance_score REAL NOT NULL DEFAULT 0.5,
    tags TEXT DEFAULT '[]',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_context_memory_type ON context_memory(type);
CREATE INDEX IF NOT EXISTS idx_context_memory_person ON context_memory(person);
CREATE INDEX IF NOT EXISTS idx_context_memory_confidence ON context_memory(confidence);
CREATE INDEX IF NOT EXISTS idx_context_memory_source_session ON context_memory(source_session);
CREATE INDEX IF NOT EXISTS idx_context_memory_status ON context_memory(confirmed, suggested);
`

func NewStore(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("init memory schema: %w", err)
	}
	// Add the disputed column for databases created before it existed.
	if err := dbpkg.AddColumnIfMissing(db, "context_memory", "disputed", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return nil, fmt.Errorf("memory schema upgrade: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Save(entry MemoryEntry) (MemoryEntry, error) {
	if entry.ID == "" {
		id, err := genID("mem")
		if err != nil {
			return MemoryEntry{}, fmt.Errorf("save memory: gen id: %w", err)
		}
		entry.ID = id
	}
	now := time.Now().UnixNano()
	if entry.CreatedAt == 0 {
		entry.CreatedAt = now
	}
	// Preserve a caller-provided UpdatedAt (backup restore) so the decay reference
	// isn't reset to "now"; only stamp it for fresh saves.
	if entry.UpdatedAt == 0 {
		entry.UpdatedAt = now
	}
	if entry.Tags == nil {
		entry.Tags = []string{}
	}

	tagsJSON := tagsToString(entry.Tags)
	confirmed := boolToInt(entry.Confirmed)
	suggested := boolToInt(entry.Suggested)
	disputed := boolToInt(entry.Disputed)

	_, err := s.db.Exec(
		`INSERT INTO context_memory (id, type, person, content, confidence, source_session,
		 confirmed, suggested, disputed, decay_rate, relevance_score, tags, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		     type = excluded.type,
		     person = excluded.person,
		     content = excluded.content,
		     confidence = excluded.confidence,
		     source_session = excluded.source_session,
		     confirmed = excluded.confirmed,
		     suggested = excluded.suggested,
		     disputed = excluded.disputed,
		     decay_rate = excluded.decay_rate,
		     relevance_score = excluded.relevance_score,
		     tags = excluded.tags,
		     updated_at = excluded.updated_at`,
		entry.ID, string(entry.Type), entry.Person, entry.Content, entry.Confidence,
		entry.SourceSession, confirmed, suggested, disputed, entry.DecayRate, entry.RelevanceScore,
		tagsJSON, entry.CreatedAt, entry.UpdatedAt,
	)
	if err != nil {
		return MemoryEntry{}, fmt.Errorf("save memory: %w", err)
	}
	s.writeMarkdown(entry)
	return entry, nil
}

// SaveImported persists a restored memory additively: existing rows are never
// overwritten and the userdata/memory/*.md journals are not regenerated (they ship
// in the archive, and re-rendering would clobber local edits).
func (s *Store) SaveImported(entry MemoryEntry) (MemoryEntry, error) {
	if entry.ID == "" {
		id, err := genID("mem")
		if err != nil {
			return MemoryEntry{}, fmt.Errorf("save imported memory: gen id: %w", err)
		}
		entry.ID = id
	}
	if entry.Tags == nil {
		entry.Tags = []string{}
	}
	tagsJSON := tagsToString(entry.Tags)
	_, err := s.db.Exec(
		`INSERT INTO context_memory (id, type, person, content, confidence, source_session,
		 confirmed, suggested, disputed, decay_rate, relevance_score, tags, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO NOTHING`,
		entry.ID, string(entry.Type), entry.Person, entry.Content, entry.Confidence,
		entry.SourceSession, boolToInt(entry.Confirmed), boolToInt(entry.Suggested),
		boolToInt(entry.Disputed), entry.DecayRate, entry.RelevanceScore,
		tagsJSON, entry.CreatedAt, entry.UpdatedAt,
	)
	if err != nil {
		return MemoryEntry{}, fmt.Errorf("save imported memory: %w", err)
	}
	return entry, nil
}

func (s *Store) Get(id string) (MemoryEntry, bool) {
	var e MemoryEntry
	var typ string
	var confirmed, suggested int
	var tagsJSON string

	err := s.db.QueryRow(
		`SELECT id, type, person, content, confidence, source_session,
		 confirmed, suggested, disputed, decay_rate, relevance_score, tags, created_at, updated_at
		 FROM context_memory WHERE id = ?`, id,
	).Scan(&e.ID, &typ, &e.Person, &e.Content, &e.Confidence, &e.SourceSession,
		&confirmed, &suggested, &e.Disputed, &e.DecayRate, &e.RelevanceScore, &tagsJSON, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		if err != sql.ErrNoRows {
			log.Printf("memory: Get %q: %v", id, err)
		}
		return MemoryEntry{}, false
	}

	e.Type = MemoryType(typ)
	e.Confirmed = confirmed != 0
	e.Suggested = suggested != 0
	e.Tags = stringToTags(tagsJSON)
	return e, true
}

func (s *Store) Count() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM context_memory`).Scan(&n)
	return n, err
}

func (s *Store) ListAll() ([]MemoryEntry, error) {
	rows, err := s.db.Query(
		`SELECT id, type, person, content, confidence, source_session,
		 confirmed, suggested, disputed, decay_rate, relevance_score, tags, created_at, updated_at
		 FROM context_memory ORDER BY created_at DESC LIMIT 1000`,
	)
	if err != nil {
		return nil, fmt.Errorf("list all memories: %w", err)
	}
	defer rows.Close()
	return scanMemories(rows)
}

type FilterOpts struct {
	Type       string
	Person     string
	MinConf    float64
	HasMinConf bool
	Limit      int
	Offset     int
}

// ListFiltered pushes type/person/confidence filters and limit/offset into SQL,
// avoiding a full-table scan.
func (s *Store) ListFiltered(f FilterOpts) ([]MemoryEntry, error) {
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}

	var conds []string
	var args []any

	if f.Type != "" {
		conds = append(conds, "type = ?")
		args = append(args, f.Type)
	}
	if f.Person != "" {
		conds = append(conds, "person = ?")
		args = append(args, f.Person)
	}
	if f.HasMinConf {
		conds = append(conds, "confidence >= ?")
		args = append(args, f.MinConf)
	}

	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	args = append(args, limit, f.Offset)
	rows, err := s.db.Query(
		`SELECT id, type, person, content, confidence, source_session,
		 confirmed, suggested, disputed, decay_rate, relevance_score, tags, created_at, updated_at
		 FROM context_memory`+where+` ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("list filtered memories: %w", err)
	}
	defer rows.Close()
	return scanMemories(rows)
}

// CountFiltered counts memories matching the same filters as ListFiltered
// (ignoring Limit/Offset) so handlers can report an accurate pagination total.
func (s *Store) CountFiltered(f FilterOpts) (int, error) {
	var conds []string
	var args []any
	if f.Type != "" {
		conds = append(conds, "type = ?")
		args = append(args, f.Type)
	}
	if f.Person != "" {
		conds = append(conds, "person = ?")
		args = append(args, f.Person)
	}
	if f.HasMinConf {
		conds = append(conds, "confidence >= ?")
		args = append(args, f.MinConf)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM context_memory`+where, args...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count filtered memories: %w", err)
	}
	return n, nil
}

func (s *Store) ListByType(mt MemoryType) ([]MemoryEntry, error) {
	rows, err := s.db.Query(
		`SELECT id, type, person, content, confidence, source_session,
		 confirmed, suggested, disputed, decay_rate, relevance_score, tags, created_at, updated_at
		 FROM context_memory WHERE type = ? ORDER BY confidence DESC`,
		string(mt),
	)
	if err != nil {
		return nil, fmt.Errorf("list by type: %w", err)
	}
	defer rows.Close()
	return scanMemories(rows)
}

func (s *Store) ListByPerson(person string) ([]MemoryEntry, error) {
	rows, err := s.db.Query(
		`SELECT id, type, person, content, confidence, source_session,
		 confirmed, suggested, disputed, decay_rate, relevance_score, tags, created_at, updated_at
		 FROM context_memory WHERE person = ? ORDER BY confidence DESC`,
		person,
	)
	if err != nil {
		return nil, fmt.Errorf("list by person: %w", err)
	}
	defer rows.Close()
	return scanMemories(rows)
}

func (s *Store) ListConfirmed() ([]MemoryEntry, error) {
	rows, err := s.db.Query(
		`SELECT id, type, person, content, confidence, source_session,
		 confirmed, suggested, disputed, decay_rate, relevance_score, tags, created_at, updated_at
		 FROM context_memory WHERE confirmed = 1 ORDER BY relevance_score DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list confirmed: %w", err)
	}
	defer rows.Close()
	return scanMemories(rows)
}

func (s *Store) ListUnconfirmed() ([]MemoryEntry, error) {
	rows, err := s.db.Query(
		`SELECT id, type, person, content, confidence, source_session,
		 confirmed, suggested, disputed, decay_rate, relevance_score, tags, created_at, updated_at
		 FROM context_memory WHERE confirmed = 0 AND suggested = 1 ORDER BY confidence DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list unconfirmed: %w", err)
	}
	defer rows.Close()
	return scanMemories(rows)
}

func (s *Store) Confirm(id string) error {
	res, err := s.db.Exec(`UPDATE context_memory SET confirmed = 1, suggested = 0, disputed = 0, updated_at = ? WHERE id = ?`,
		time.Now().UnixNano(), id)
	if err != nil {
		return fmt.Errorf("confirm memory %q: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm memory %q rows: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("confirm memory: id %q not found", id)
	}
	s.refreshMarkdown(id)
	return nil
}

func (s *Store) UpdateConfidence(id string, confidence float64) error {
	// NaN slips past both comparisons; normalize it like any out-of-domain value.
	if math.IsNaN(confidence) || confidence < 0 {
		confidence = 0
	}
	if confidence > 1 {
		confidence = 1
	}
	now := time.Now().UnixNano()
	_, err := s.db.Exec(
		"UPDATE context_memory SET confidence = ?, updated_at = ? WHERE id = ?",
		confidence, now, id,
	)
	if err != nil {
		return fmt.Errorf("update confidence %q: %w", id, err)
	}
	return nil
}

// Dispute marks a memory inaccurate: clears confirmed/suggested, sets disputed,
// and halves confidence so it decays out faster and de-prioritizes in context.
func (s *Store) Dispute(id string) error {
	now := time.Now().UnixNano()
	res, err := s.db.Exec(
		"UPDATE context_memory SET confirmed = 0, suggested = 0, disputed = 1, confidence = confidence * 0.5, updated_at = ? WHERE id = ?",
		now, id,
	)
	if err != nil {
		return fmt.Errorf("dispute memory %q: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("memory %q not found", id)
	}
	s.refreshMarkdown(id)
	return nil
}

func (s *Store) Delete(id string) error {
	e, ok := s.Get(id)
	result, err := s.db.Exec("DELETE FROM context_memory WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete memory: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("memory %q not found", id)
	}
	if ok {
		s.writeMarkdown(e)
	}
	return nil
}

// refreshMarkdown rewrites the month's journal so confirm/dispute show on disk.
func (s *Store) refreshMarkdown(id string) {
	if MarkdownDir == "" {
		return
	}
	e, ok := s.Get(id)
	if !ok {
		return
	}
	s.writeMarkdown(e)
}

// ClearSessionRef removes the source_session back-reference (memories kept) when a
// session is deleted. The column is NOT NULL, so ” is the "no session" sentinel.
func (s *Store) ClearSessionRef(sessionID string) error {
	_, err := s.db.Exec(
		`UPDATE context_memory SET source_session = '' WHERE source_session = ?`, sessionID,
	)
	if err != nil {
		return fmt.Errorf("clear session ref %q: %w", sessionID, err)
	}
	return nil
}

// FindSimilarByContent returns memories matching any significant token of the query,
// best matches first. Token (CJK bigram + word) matching recalls relevant memories
// where the old whole-substring LIKE found nothing verbatim.
func (s *Store) FindSimilarByContent(query string, limit int) ([]MemoryEntry, error) {
	all, err := s.ListAll()
	if err != nil {
		return nil, fmt.Errorf("find similar: %w", err)
	}
	tokens := memoryQueryTokens(query)
	if len(tokens) == 0 {
		return nil, nil
	}
	type scored struct {
		e    MemoryEntry
		hits int
	}
	var hits []scored
	for _, e := range all {
		hay := strings.ToLower(e.Content + " " + e.Person)
		n := 0
		for _, t := range tokens {
			if strings.Contains(hay, t) {
				n++
			}
		}
		if n > 0 {
			hits = append(hits, scored{e: e, hits: n})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].hits != hits[j].hits {
			return hits[i].hits > hits[j].hits
		}
		return hits[i].e.Confidence > hits[j].e.Confidence
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]MemoryEntry, len(hits))
	for i, h := range hits {
		out[i] = h.e
	}
	return out, nil
}

// memoryQueryTokens extracts CJK bigrams and 2+ char words, deduped and capped.
func memoryQueryTokens(q string) []string {
	const maxTokens = 100
	seen := make(map[string]bool)
	var out []string
	var cjk []rune
	var word []rune
	flushCJK := func() {
		for i := 0; i+1 < len(cjk); i++ {
			t := string(cjk[i : i+2])
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
		cjk = cjk[:0]
	}
	flushWord := func() {
		if len(word) >= 2 {
			t := strings.ToLower(string(word))
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
		word = word[:0]
	}
	for _, r := range q {
		switch {
		case unicode.Is(unicode.Han, r):
			flushWord()
			cjk = append(cjk, r)
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			flushCJK()
			word = append(word, r)
		default:
			flushCJK()
			flushWord()
		}
		if len(out) >= maxTokens {
			return out
		}
	}
	flushCJK()
	flushWord()
	if len(out) > maxTokens {
		out = out[:maxTokens]
	}
	return out
}

func scanMemories(rows *sql.Rows) ([]MemoryEntry, error) {
	result := make([]MemoryEntry, 0)
	for rows.Next() {
		var e MemoryEntry
		var typ string
		var confirmed, suggested, disputed int
		var tagsJSON string

		if err := rows.Scan(
			&e.ID, &typ, &e.Person, &e.Content, &e.Confidence, &e.SourceSession,
			&confirmed, &suggested, &disputed, &e.DecayRate, &e.RelevanceScore, &tagsJSON,
			&e.CreatedAt, &e.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		e.Type = MemoryType(typ)
		e.Confirmed = confirmed != 0
		e.Suggested = suggested != 0
		e.Disputed = disputed != 0
		e.Tags = stringToTags(tagsJSON)
		result = append(result, e)
	}
	return result, rows.Err()
}

func genID(prefix string) (string, error) {
	b := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s_%x", prefix, b), nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func tagsToString(tags []string) string {
	if len(tags) == 0 {
		return "[]"
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func stringToTags(s string) []string {
	if s == "" || s == "[]" {
		return []string{}
	}
	var tags []string
	if err := json.Unmarshal([]byte(s), &tags); err != nil {
		return []string{}
	}
	return tags
}

// ListAllForBackup returns every memory with no row cap, for backup export.
func (s *Store) ListAllForBackup() ([]MemoryEntry, error) {
	rows, err := s.db.Query(
		`SELECT id, type, person, content, confidence, source_session,
		 confirmed, suggested, disputed, decay_rate, relevance_score, tags, created_at, updated_at
		 FROM context_memory ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list all for backup: %w", err)
	}
	defer rows.Close()
	return scanMemories(rows)
}
