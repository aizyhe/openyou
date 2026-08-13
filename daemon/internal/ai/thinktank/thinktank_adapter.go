package thinktank

import (
	"openyou/daemon/internal/ai/agent"
	"openyou/daemon/internal/store/memory"
)

type SkillMatcherAdapter struct {
	matcher agent.SkillMatcher
}

func NewSkillAdapter(m agent.SkillMatcher) *SkillMatcherAdapter {
	return &SkillMatcherAdapter{matcher: m}
}

func (a *SkillMatcherAdapter) Match(query string) []SkillInfo {
	matched := a.matcher.Match(query)
	out := make([]SkillInfo, len(matched))
	for i, m := range matched {
		out[i] = SkillInfo{
			Name:        m.Name,
			Description: m.Description,
			Path:        m.Path,
			Deps:        m.Deps,
			Content:     m.Content,
		}
	}
	return out
}

type MemoryAdapter struct {
	store *memory.Store
}

func NewMemoryAdapter(s *memory.Store) *MemoryAdapter {
	return &MemoryAdapter{store: s}
}

func (a *MemoryAdapter) Relevant(query string, limit int) []string {
	if a.store == nil {
		return nil
	}
	mems, err := a.store.FindSimilarByContent(query, limit)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(mems))
	for _, m := range mems {
		line := m.Content
		if m.Person != "" {
			line = m.Person + "：" + line
		}
		out = append(out, line)
	}
	return out
}
