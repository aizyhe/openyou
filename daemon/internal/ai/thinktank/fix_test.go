package thinktank

import (
	"strings"
	"testing"
)

// C10: when on-demand retrieval for an inside sub-agent is entirely empty,
// fall back to injecting the full archive to avoid a hollow prompt.
func TestBuildSkillPrompt_InsideFallback(t *testing.T) {
	// Empty doc source → both RelevantDocs and ReadDir return empty
	docs := &mockDocs{files: map[string]string{}}
	bt := &ThinkTank{
		llm:    &mockLLM{},
		skills: &mockSkills{},
		memory: &mockMemory{},
		docs:   docs,
	}
	sk := SkillInfo{Name: "人", Path: "inside/people/x.md", Content: "# 张建国档案\n\n- 关键内容 KEYBODY"}

	p := bt.buildSkillPrompt(sk, "问个问题")
	if !strings.Contains(p, "KEYBODY") {
		t.Errorf("on-demand retrieval empty: expected full-archive fallback with KEYBODY in prompt")
	}
}

// C10 reverse: when on-demand retrieval hits, the full text is not injected
// again (dedup), avoiding duplication.
func TestBuildSkillPrompt_InsideNoDuplicate(t *testing.T) {
	docs := &mockDocs{files: map[string]string{
		"rule/openyou.md":    "# 铁律",
		"inside/people/x.md": "# 张建国档案\n\n- 关键内容 KEYBODY",
	}}
	bt := &ThinkTank{
		llm:    &mockLLM{},
		skills: &mockSkills{},
		memory: &mockMemory{},
		docs:   docs,
	}
	sk := SkillInfo{Name: "人", Path: "inside/people/x.md", Content: "# 张建国档案\n\n- 关键内容 KEYBODY"}

	p := bt.buildSkillPrompt(sk, "张建国")
	// Retrieval hit → the relevant section should be present
	if !strings.Contains(p, "KEYBODY") {
		t.Errorf("retrieval hit: expected relevant section KEYBODY in prompt")
	}
}
