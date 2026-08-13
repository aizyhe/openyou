package memory

type MemoryType string

const (
	// MemoryPersonFact records facts about people ("Alice recently started focusing on cost control").
	MemoryPersonFact MemoryType = "person_fact"

	// MemoryUserTrait records the user's preferences, habits, or patterns.
	MemoryUserTrait MemoryType = "user_trait"

	// MemoryRule: behavioral rules from experience (e.g. "Friday messages get low reply rates").
	MemoryRule MemoryType = "rule"

	// MemoryContext records current projects, goals, or situational context ("Preparing Q3 tech report").
	MemoryContext MemoryType = "context"
)

// MemoryEntry: SourceSession == "" means "no source session" (omitempty keeps it
// absent in JSON so frontends can tell present from absent); the DB column is
// NOT NULL DEFAULT ” to match.
type MemoryEntry struct {
	ID             string     `json:"id"`
	Type           MemoryType `json:"type"`
	Person         string     `json:"person"`
	Content        string     `json:"content"`
	Confidence     float64    `json:"confidence"`
	SourceSession  string     `json:"source_session,omitempty"`
	Confirmed      bool       `json:"confirmed"`
	Suggested      bool       `json:"suggested"`
	Disputed       bool       `json:"disputed"`
	DecayRate      float64    `json:"decay_rate"`
	RelevanceScore float64    `json:"relevance_score"`
	Tags           []string   `json:"tags"`
	CreatedAt      int64      `json:"created_at"`
	UpdatedAt      int64      `json:"updated_at"`
}

func NewMemoryEntry(typ MemoryType, content string, confidence float64, sourceSession string) MemoryEntry {
	return MemoryEntry{
		Type:           typ,
		Content:        content,
		Confidence:     confidence,
		SourceSession:  sourceSession,
		DecayRate:      0.01,
		RelevanceScore: 0.5,
		Tags:           []string{},
	}
}

// ConfidenceDecision groups entries by confidence level using per-type auto-save
// thresholds (person_fact ≥ 0.80, user_trait ≥ 0.85, rule ≥ 0.75, context ≥ 0.80;
// unknown types default to 0.85).
type ConfidenceDecision struct {
	// HighConfidence entries (confidence ≥ their type threshold) are auto-saved.
	HighConfidence []MemoryEntry

	// MediumConfidence entries (0.60 ≤ c < type threshold) are flagged for user confirmation.
	MediumConfidence []MemoryEntry

	// LowConfidence entries (< 0.60) are discarded.
	LowConfidence []MemoryEntry

	Conflicts []Conflict
}

type Conflict struct {
	New      MemoryEntry `json:"new"`
	Existing MemoryEntry `json:"existing"`
	Reason   string      `json:"reason"`
}
