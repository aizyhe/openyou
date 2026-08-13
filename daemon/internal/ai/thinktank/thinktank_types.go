package thinktank

import "context"

type AnalysisEventType string

const (
	EventThinkTankStart AnalysisEventType = "thinktank-start"
	EventSkillProgress  AnalysisEventType = "skill-progress"
	EventSkillResult    AnalysisEventType = "skill-result"
	EventSynthesisDelta AnalysisEventType = "synthesis-delta"
	EventConflict       AnalysisEventType = "conflict"
	EventCitation       AnalysisEventType = "source-citation"
	EventDone           AnalysisEventType = "done"
	EventError          AnalysisEventType = "error"
)

type AnalysisEvent struct {
	Type        AnalysisEventType `json:"type"`
	Skills      []SkillBrief      `json:"skills,omitempty"`
	Skill       string            `json:"skill,omitempty"`
	Status      string            `json:"status,omitempty"`
	Result      string            `json:"result,omitempty"`
	Text        string            `json:"text,omitempty"`
	SkillA      string            `json:"skill_a,omitempty"`
	SkillB      string            `json:"skill_b,omitempty"`
	Description string            `json:"description,omitempty"`
	Source      string            `json:"source,omitempty"`
	SourceType  string            `json:"source_type,omitempty"`
	Error       string            `json:"error,omitempty"`
}

type SkillBrief struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// SkillInfo: inside/ docs are injected on demand via RelevantDocs, not duplicated in Content.
type SkillInfo struct {
	Name        string
	Description string
	Path        string
	Deps        []string
	Content     string
}

type LLMClient interface {
	Chat(ctx context.Context, systemPrompt, userMessage string) (string, error)
	StreamChat(ctx context.Context, systemPrompt, userMessage string, onDelta func(string)) (string, error)
}

type SkillProvider interface {
	Match(query string) []SkillInfo
}

// DocProvider reads context-tree documents by relative path (a skill's `deps:`).
// ctxpkg.Matcher satisfies it implicitly; nil means no deps.
type DocProvider interface {
	ReadDoc(relpath string) (string, error)
	ListDocs() []string
	// ReadDir merges all .md files under a fixed top-level dir (e.g. "rule",
	// "skill") — multi-file, freely-named docs under one contract directory.
	ReadDir(dir string) (string, error)
	// RelevantDocs is the on-demand retrieval primitive; empty when nothing matches.
	RelevantDocs(query string, budget int) []string
}

// MemoryProvider: nil means memories are not injected.
type MemoryProvider interface {
	Relevant(query string, limit int) []string
}
