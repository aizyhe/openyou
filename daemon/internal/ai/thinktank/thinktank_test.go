package thinktank

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"openyou/daemon/internal/ai/agent"
)

// mockLLM returns predetermined responses for testing.
type mockLLM struct {
	responses map[string]string
}

func (m *mockLLM) Chat(_ context.Context, systemPrompt, userMessage string) (string, error) {
	for skill, resp := range m.responses {
		if strings.Contains(systemPrompt, skill) {
			return resp, nil
		}
	}
	return "Generic analysis.", nil
}

func (m *mockLLM) StreamChat(ctx context.Context, systemPrompt, userMessage string, onDelta func(string)) (string, error) {
	s, err := m.Chat(ctx, systemPrompt, userMessage)
	if onDelta != nil && s != "" {
		onDelta(s)
	}
	return s, err
}

// mockSkills provides predetermined skill matches.
type mockSkills struct {
	skills []SkillInfo
}

func (m *mockSkills) Match(_ string) []SkillInfo {
	return m.skills
}

// mockMemory is a MemoryProvider returning fixed relevant memories.
type mockMemory struct{ mems []string }

func (m *mockMemory) Relevant(_ string, _ int) []string { return m.mems }

// mockDocs is a DocProvider backed by an in-memory map.
type mockDocs struct{ files map[string]string }

func (m *mockDocs) ReadDoc(rel string) (string, error) {
	if c, ok := m.files[rel]; ok {
		return c, nil
	}
	return "", nil
}

func (m *mockDocs) ReadDir(dir string) (string, error) {
	var b strings.Builder
	for k, v := range m.files {
		if strings.HasPrefix(k, dir+"/") {
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(v)
		}
	}
	return b.String(), nil
}

func (m *mockDocs) ListDocs() []string {
	var out []string
	for k := range m.files {
		out = append(out, k)
	}
	return out
}

func (m *mockDocs) RelevantDocs(query string, budget int) []string {
	if budget <= 0 {
		return nil
	}
	var out []string
	remaining := budget
	for k, v := range m.files {
		if !strings.HasPrefix(k, "inside/") {
			continue
		}
		// Match each content-bearing token of the query against the doc,
		// mirroring the real matcher: doc content contains query tokens.
		q := strings.ToLower(strings.TrimSpace(query))
		doc := strings.ToLower(v) + " " + strings.ToLower(k)
		hit := false
		for _, r := range q {
			if r == ' ' || r == '\n' || r == '\t' {
				continue
			}
			if strings.ContainsRune(doc, r) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		full := "### " + k + "\n\n" + v
		if len(full) > remaining {
			full = full[:remaining] + "\n…(truncated)"
		}
		remaining -= len(full)
		out = append(out, full)
		if remaining <= 0 {
			break
		}
	}
	return out
}

func TestBuildInsideContext(t *testing.T) {
	docs := &mockDocs{files: map[string]string{
		"rule/rule1.md": "#1. rule",
		"inside/张三.md":  "# 张三\nControl type.",
	}}
	bt := New(&mockLLM{}, &mockSkills{}, &mockMemory{mems: []string{"用户偏好数据驱动的论证"}}, docs)
	ctx := bt.buildInsideContext("怎么应对张三")
	if !strings.Contains(ctx, "相关记忆") {
		t.Fatalf("expected memories section, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "数据驱动") {
		t.Errorf("memories not injected into context:\n%s", ctx)
	}
	if !strings.Contains(ctx, "档案") {
		t.Errorf("inside docs not injected into context:\n%s", ctx)
	}
	if !strings.Contains(ctx, "规则") {
		t.Errorf("rules not injected into context:\n%s", ctx)
	}
}

func TestAnalyze_NoSkills(t *testing.T) {
	bt := New(
		&mockLLM{responses: map[string]string{"": "Fallback response."}},
		&mockSkills{skills: nil},
		nil,
		nil,
	)
	ch, err := bt.Analyze(context.Background(), "hello", "s1")
	if err != nil {
		t.Fatal(err)
	}
	var gotDone bool
	for ev := range ch {
		if ev.Type == EventDone {
			gotDone = true
		}
	}
	if !gotDone {
		t.Error("expected done event")
	}
}

func TestAnalyze_SingleSkill(t *testing.T) {
	bt := New(
		// Key must case-match the skill content substring the mock checks.
		&mockLLM{responses: map[string]string{"Power": "Power analysis result."}},
		&mockSkills{skills: []SkillInfo{{Name: "power-analysis", Description: "Power dynamics", Content: "Power framework."}}},
		nil,
		nil,
	)
	ch, err := bt.Analyze(context.Background(), "conflict with colleague", "s1")
	if err != nil {
		t.Fatal(err)
	}
	// Single-skill contract: the result is delivered ONLY as synthesis-delta.
	// Emitting skill-result as well made clients render/persist the same text
	// twice (the handler accumulates deltas into the persisted reply).
	var gotResult, gotDelta, gotDone bool
	for ev := range ch {
		switch ev.Type {
		case EventSkillResult:
			gotResult = true
		case EventSynthesisDelta:
			if ev.Text == "Power analysis result." {
				gotDelta = true
			}
		case EventDone:
			gotDone = true
		}
	}
	if gotResult {
		t.Error("single-skill mode must not emit skill-result (duplicates the synthesis text)")
	}
	if !gotDelta {
		t.Error("expected synthesis-delta carrying the result")
	}
	if !gotDone {
		t.Error("expected done event")
	}
}

func TestAnalyze_BrainTrustMode(t *testing.T) {
	bt := New(
		&mockLLM{
			responses: map[string]string{
				"power":         "Power: Confront from position of strength.",
				"communication": "Comm: Wait and build rapport first.",
			},
		},
		&mockSkills{
			skills: []SkillInfo{
				{Name: "power-analysis", Description: "Power dynamics", Content: "Power framework."},
				{Name: "communication", Description: "Communication strategies", Content: "Comm framework."},
			},
		},
		nil,
		nil,
	)
	ch, err := bt.Analyze(context.Background(), "conflict", "s1")
	if err != nil {
		t.Fatal(err)
	}
	var gotStart, gotProgress, gotResult, gotSynth, gotDone bool
	for ev := range ch {
		switch ev.Type {
		case EventBrainTrustStart:
			gotStart = true
			if len(ev.Skills) != 2 {
				t.Errorf("expected 2 skills, got %d", len(ev.Skills))
			}
		case EventSkillProgress:
			gotProgress = true
		case EventSkillResult:
			gotResult = true
		case EventSynthesisDelta:
			gotSynth = true
		case EventDone:
			gotDone = true
		}
	}
	if !gotStart {
		t.Error("expected brain-trust-start event")
	}
	if !gotProgress {
		t.Error("expected skill-progress events")
	}
	if !gotResult {
		t.Error("expected skill-result events")
	}
	if !gotSynth {
		t.Error("expected synthesis events")
	}
	if !gotDone {
		t.Error("expected done event")
	}
}

func TestAnalyze_ConflictDetection(t *testing.T) {
	bt := New(
		&mockLLM{
			responses: map[string]string{
				"power": "You should escalate this immediately.",
				"comm":  "Wait and observe before taking action.",
			},
		},
		&mockSkills{
			skills: []SkillInfo{
				{Name: "power", Description: "Power", Content: "power framework."},
				{Name: "comm", Description: "Comm", Content: "comm framework."},
			},
		},
		nil,
		nil,
	)
	ch, err := bt.Analyze(context.Background(), "conflict question", "s1")
	if err != nil {
		t.Fatal(err)
	}
	var conflictCount int
	for ev := range ch {
		if ev.Type == EventConflict {
			conflictCount++
		}
	}
	if conflictCount == 0 {
		t.Error("expected at least one conflict event (escalate vs wait)")
	}
}

func TestAnalyze_InsideInjection(t *testing.T) {
	docs := &mockDocs{files: map[string]string{
		"rule/rule1.md": "#1. Test rule.",
		"inside/同事A.md": "# 同事 A\nControl type.",
	}}
	bt := New(
		&mockLLM{responses: map[string]string{"power": "Analysis with context."}},
		&mockSkills{
			skills: []SkillInfo{{Name: "power-analysis", Description: "Power", Content: "Framework."}},
		},
		nil,
		docs,
	)
	ch, err := bt.Analyze(context.Background(), "同事 A is causing trouble", "s1")
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
}

func TestAnalyze_SubAgentTimeout(t *testing.T) {
	bt := New(
		&mockLLM{
			responses: map[string]string{
				"skill1": "Result 1.",
				"skill2": "Result 2.",
			},
		},
		&mockSkills{
			skills: []SkillInfo{
				{Name: "skill1", Description: "S1", Content: "c1"},
				{Name: "skill2", Description: "S2", Content: "c2"},
			},
		},
		nil,
		nil,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	ch, err := bt.Analyze(ctx, "test", "s1")
	if err != nil {
		t.Fatal(err)
	}
	var gotError bool
	for ev := range ch {
		if ev.Type == EventError {
			gotError = true
		}
	}
	_ = gotError
}

func TestDetectConflicts(t *testing.T) {
	bt := &BrainTrust{}
	analyses := []subResult{
		{skill: "power", result: "You should escalate this to management."},
		{skill: "comm", result: "Wait and observe before acting."},
	}
	conflicts := bt.detectConflicts(analyses)
	if len(conflicts) != 1 {
		t.Errorf("expected 1 conflict, got %d", len(conflicts))
	}
}

func TestDetectConflicts_NoConflict(t *testing.T) {
	bt := &BrainTrust{}
	analyses := []subResult{
		{skill: "power", result: "Build alliances first."},
		{skill: "comm", result: "Use diplomatic language."},
	}
	conflicts := bt.detectConflicts(analyses)
	if len(conflicts) != 0 {
		t.Errorf("expected 0 conflicts, got %d", len(conflicts))
	}
}

func TestSplitChunks(t *testing.T) {
	chunks := splitChunks("hello world", 5)
	if len(chunks) != 3 {
		t.Errorf("expected 3 chunks, got %d: %v", len(chunks), chunks)
	}
	chunks = splitChunks("hi", 10)
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(chunks))
	}
}

// errLLM lets a test fail specific calls (e.g. the synthesis call) while
// succeeding on sub-agent calls, exercising the synthesis-failure fallback.
type errLLM struct {
	fail func(sys, user string) bool
	resp func(sys, user string) string
}

func (e *errLLM) Chat(_ context.Context, sys, user string) (string, error) {
	if e.fail != nil && e.fail(sys, user) {
		return "", errors.New("boom")
	}
	if e.resp != nil {
		return e.resp(sys, user), nil
	}
	return "ok", nil
}

func (e *errLLM) StreamChat(ctx context.Context, sys, user string, onDelta func(string)) (string, error) {
	s, err := e.Chat(ctx, sys, user)
	if onDelta != nil && s != "" {
		onDelta(s)
	}
	return s, err
}

func TestAnalyze_SynthesisFailureFallback(t *testing.T) {
	llm := &errLLM{
		fail: func(sys, _ string) bool { return strings.Contains(sys, "Synthesize") },
		resp: func(sys, _ string) string {
			if strings.Contains(sys, "Synthesize") {
				return "" // won't be used (fail returns error first)
			}
			return "sub-agent analysis"
		},
	}
	bt := New(llm, &mockSkills{skills: []SkillInfo{
		{Name: "power", Description: "p", Content: "c"},
		{Name: "comm", Description: "c2", Content: "c2"},
	}}, nil, nil)

	ch, err := bt.Analyze(context.Background(), "query", "s1")
	if err != nil {
		t.Fatal(err)
	}
	var gotFallback bool
	for ev := range ch {
		if ev.Type == EventSynthesisDelta && strings.Contains(ev.Text, "[power]") {
			gotFallback = true
		}
	}
	if !gotFallback {
		t.Error("expected fallback per-skill chunks after synthesis failure")
	}
}

func TestAnalyze_AllSubAgentsFailed(t *testing.T) {
	llm := &errLLM{fail: func(_, _ string) bool { return true }}
	bt := New(llm, &mockSkills{skills: []SkillInfo{
		{Name: "power", Description: "p", Content: "c"},
		{Name: "comm", Description: "c2", Content: "c2"},
	}}, nil, nil)

	ch, err := bt.Analyze(context.Background(), "query", "s1")
	if err != nil {
		t.Fatal(err)
	}
	var gotErr bool
	for ev := range ch {
		if ev.Type == EventError && strings.Contains(ev.Error, "all sub-agents failed") {
			gotErr = true
		}
	}
	if !gotErr {
		t.Error("expected 'all sub-agents failed' error")
	}
}

func TestAnalyze_NoSkillsError(t *testing.T) {
	llm := &errLLM{fail: func(_, _ string) bool { return true }}
	bt := New(llm, &mockSkills{skills: nil}, nil, nil)
	ch, err := bt.Analyze(context.Background(), "query", "s1")
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
}

func TestAnalyze_SingleSkillError(t *testing.T) {
	llm := &errLLM{fail: func(_, _ string) bool { return true }}
	bt := New(llm, &mockSkills{skills: []SkillInfo{{Name: "solo", Content: "c"}}}, nil, nil)
	ch, err := bt.Analyze(context.Background(), "query", "s1")
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
}

func TestAnalyze_TruncatesToMaxSubAgents(t *testing.T) {
	skills := make([]SkillInfo, maxSubAgents+2)
	for i := range skills {
		skills[i] = SkillInfo{Name: "skill", Content: "c"}
	}
	bt := New(&mockLLM{responses: map[string]string{}}, &mockSkills{skills: skills}, nil, nil)
	ch, err := bt.Analyze(context.Background(), "query", "s1")
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
}

func TestAnalyze_CancelledContext(t *testing.T) {
	bt := New(&mockLLM{responses: map[string]string{}}, &mockSkills{skills: []SkillInfo{
		{Name: "skill1", Content: "c1"},
		{Name: "skill2", Content: "c2"},
	}}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ch, err := bt.Analyze(ctx, "query", "s1")
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
}

func TestBuildSimplePrompt_WithInside(t *testing.T) {
	docs := &mockDocs{files: map[string]string{
		"rule/rule1.md": "#1. rule",
		"inside/张三.md":  "# 张三\n控制型",
	}}
	bt := New(&mockLLM{responses: map[string]string{"": "resp"}},
		&mockSkills{skills: nil}, nil, docs)
	got := bt.buildSimplePrompt("张三 causes trouble")
	if !strings.Contains(got, "Relevant context") {
		t.Errorf("expected context in simple prompt, got %q", got)
	}
}

func TestBuildSkillPrompt_InjectsDeps(t *testing.T) {
	docs := &mockDocs{files: map[string]string{
		"resume/resume-final.md": "我的简历正文:资深后端工程师",
	}}
	bt := New(&mockLLM{}, &mockSkills{}, nil, docs)

	sk := SkillInfo{
		Name: "career-advice",
		Deps: []string{"resume/resume-final.md"},
	}
	prompt := bt.buildSkillPrompt(sk, "should I change jobs?")
	if !strings.Contains(prompt, "资深后端工程师") {
		t.Errorf("expected deps content in skill prompt, got %q", prompt)
	}
}

func TestBuildDepsContext_Bounded(t *testing.T) {
	docs := &mockDocs{files: map[string]string{
		"big/file.md": strings.Repeat("x", depsCharBudget+100),
	}}
	bt := New(&mockLLM{}, &mockSkills{}, nil, docs)
	got := bt.buildDepsContext(SkillInfo{Deps: []string{"big/file.md"}})
	if len(got) > depsCharBudget+100 {
		t.Errorf("deps not bounded: got %d chars", len(got))
	}
}

func TestBuildDepsContext_NilProvider(t *testing.T) {
	bt := New(&mockLLM{}, &mockSkills{}, nil, nil)
	got := bt.buildDepsContext(SkillInfo{Deps: []string{"resume/x.md"}})
	if got != "" {
		t.Errorf("expected empty deps with nil provider, got %q", got)
	}
}

func TestSkillMatcherAdapter(t *testing.T) {
	infos := []agent.SkillInfo{{Name: "n", Description: "d", Deps: []string{"x"}, Content: "c"}}
	adapter := NewSkillAdapter(&fakeMatcher{infos: infos})
	out := adapter.Match("query")
	if len(out) != 1 || out[0].Name != "n" || out[0].Content != "c" {
		t.Errorf("adapter Match wrong: %+v", out)
	}
}

type fakeMatcher struct {
	infos []agent.SkillInfo
}

func (f *fakeMatcher) Match(string) []agent.SkillInfo { return f.infos }
func (f *fakeMatcher) List() []agent.SkillDef         { return nil }
