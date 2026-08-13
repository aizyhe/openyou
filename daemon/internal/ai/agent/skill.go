// Package agent holds the shared skill types and matcher contract in a leaf package
// so skills/, thinktank/, and server/ share them without importing each other.
package agent

type SkillInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Path        string   `json:"path,omitempty"`
	Deps        []string `json:"deps,omitempty"`
	Content     string   `json:"content"`
}

type SkillDef struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Path        string   `json:"path,omitempty"`
	Triggers    []string `json:"triggers"`
	Deps        []string `json:"deps,omitempty"`
	Content     string   `json:"content"`
}

type SkillMatcher interface {
	Match(userMessage string) []SkillInfo
	List() []SkillDef
}
