package matcher

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"openyou/daemon/internal/ai/agent"
)

// newTestMatcher creates a Matcher seeded with minimal inline skill fixtures.
// Skills are defined here rather than read from disk so tests are self-contained.
func newTestMatcher(t *testing.T) *Matcher {
	t.Helper()
	dir := t.TempDir()
	skills := map[string]string{
		"workplace-advisor.md":       "---\nname: workplace-advisor\ndescription: Workplace strategy\ntriggers: [职场, 工作, 晋升, 冲突, 沟通, 汇报, 谈判, 跳槽]\n---\n\n# Workplace Advisor\n\n职场策略分析框架。\n",
		"workplace-communication.md": "---\nname: workplace-communication\ndescription: Communication tactics\ntriggers: [沟通, 汇报, PPT, 演讲, 表达]\n---\n\n# Communication\n\n沟通技巧和汇报方法。\n",
		"workplace-politics.md":      "---\nname: workplace-politics\ndescription: Workplace politics\ntriggers: [政治, 冲突, 竞争, 派系, 权力]\n---\n\n# Workplace Politics\n\n职场政治应对方法。\n",
	}
	for name, content := range skills {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatalf("write skill %s: %v", name, err)
		}
	}
	m, err := NewMatcherWithDir(dir, nil)
	if err != nil {
		t.Fatalf("NewMatcherWithDir: %v", err)
	}
	return m
}

func TestNewMatcher(t *testing.T) {
	m, err := NewMatcher()
	if err != nil {
		t.Fatalf("NewMatcher failed: %v", err)
	}
	if m == nil {
		t.Fatal("NewMatcher returned nil")
	}
	// NewMatcher() returns an empty matcher — skills come from the runtime dir.
	// No embedded files: all skill documents live in service/importer/userdata/skills/.
	t.Logf("NewMatcher: %d built-in skills (expected 0)", len(m.skills))
}

func TestMatch_TriggerFound(t *testing.T) {
	m := newTestMatcher(t)
	// "冲突" triggers workplace-advisor and workplace-politics
	result := m.Match("我在职场中遇到了冲突")
	if len(result) == 0 {
		t.Error("expected at least one match for '冲突'")
	}
	for _, sk := range result {
		if sk.Content == "" {
			t.Errorf("skill %q has empty content", sk.Name)
		}
	}
}

func TestMatch_NoTrigger(t *testing.T) {
	m, err := NewMatcher()
	if err != nil {
		t.Fatal(err)
	}
	result := m.Match("今天天气怎么样")
	if len(result) != 0 {
		t.Errorf("expected no matches for irrelevant message, got %d: %v", len(result), result)
	}
}

func TestMatch_CaseInsensitive(t *testing.T) {
	m := newTestMatcher(t)
	result := m.Match("我需要做工作汇报PPT")
	if len(result) == 0 {
		t.Error("expected match for case-insensitive trigger '汇报'")
	}
}

// TestMatch_Top5ByStrength proves the "pick the 5 strongest matching skills" contract: when more
// than 5 skills match, the STRONGEST matches (most trigger hits) win the
// slots, not whichever files loaded first.
func TestMatch_Top5ByStrength(t *testing.T) {
	dir := t.TempDir()
	skills := map[string]string{
		// This skill hits 4 triggers → must rank #1.
		"alpha-strong.md": "---\nname: alpha-strong\ndescription: hits 4 triggers\ntriggers: [职场, 工作, 冲突, 汇报]\n---\n\nAlpha strong.\n",
		// These hit 1 trigger each → they fill slots 2-5.
		"bravo.md":   "---\nname: bravo\ndescription: hits 1\ntriggers: [职场]\n---\n\nBravo.\n",
		"charlie.md": "---\nname: charlie\ndescription: hits 1\ntriggers: [工作]\n---\n\nCharlie.\n",
		"delta.md":   "---\nname: delta\ndescription: hits 1\ntriggers: [冲突]\n---\n\nDelta.\n",
		"echo.md":    "---\nname: echo\ndescription: hits 1\ntriggers: [汇报]\n---\n\nEcho.\n",
		// This also hits 1 trigger but must be EXCLUDED — 6th strongest.
		"foxtrot.md": "---\nname: foxtrot\ndescription: hits 1\ntriggers: [沟通]\n---\n\nFoxtrot.\n",
	}
	for name, content := range skills {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatalf("write skill %s: %v", name, err)
		}
	}
	m, err := NewMatcherWithDir(dir, nil)
	if err != nil {
		t.Fatalf("NewMatcherWithDir: %v", err)
	}
	// Message hits 职场/工作/冲突/汇报/沟通 → 6 skills match.
	result := m.Match("我在职场中遇到工作冲突需要汇报给领导")
	if len(result) != maxMatchedSkills {
		t.Fatalf("got %d matches, want %d (top-N cap)", len(result), maxMatchedSkills)
	}
	if result[0].Name != "alpha-strong" {
		t.Errorf("strongest skill rank #1: got %q, want alpha-strong", result[0].Name)
	}
	for _, sk := range result {
		if sk.Name == "foxtrot" {
			t.Errorf("foxtrot (weakest) should have been excluded from top %d", maxMatchedSkills)
		}
	}
}

func TestMatch_EmptyMessage(t *testing.T) {
	m, err := NewMatcher()
	if err != nil {
		t.Fatal(err)
	}
	result := m.Match("")
	if len(result) != 0 {
		t.Errorf("expected no matches for empty message, got %d", len(result))
	}
}

func TestParseTriggerArray(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"[a, b, c]", []string{"a", "b", "c"}},
		{"[职场, 工作, 晋升]", []string{"职场", "工作", "晋升"}},
		{"[]", nil},
		{"[single]", []string{"single"}},
	}
	for _, tt := range tests {
		result := parseTriggerArray(tt.input)
		if len(result) != len(tt.expected) {
			t.Errorf("parseTriggerArray(%q) = %v, want %v", tt.input, result, tt.expected)
			continue
		}
		for i, v := range result {
			if v != tt.expected[i] {
				t.Errorf("parseTriggerArray(%q)[%d] = %q, want %q", tt.input, i, v, tt.expected[i])
			}
		}
	}
}

func TestParseSkill(t *testing.T) {
	raw := `---
name: test-skill
description: A test skill
triggers: [hello, test, demo]
---

# Test Skill Content

This is the skill body.`

	sk := parseSkill(raw, "test-skill.md")
	if sk.Name != "test-skill" {
		t.Errorf("name = %q, want test-skill", sk.Name)
	}
	if sk.Description != "A test skill" {
		t.Errorf("description = %q", sk.Description)
	}
	if len(sk.Triggers) != 3 {
		t.Errorf("triggers = %v, want 3 items", sk.Triggers)
	}
	// On-demand retrieval (VISION §2.8): parseSkill keeps no Content in memory —
	// it only builds a lightweight signal, and the full content is lazy-loaded on
	// Match/Get hits. Here we verify Content is empty and the signal is usable.
	if sk.Content != "" {
		t.Errorf("content should be lazily loaded (empty at parse), got %q", sk.Content)
	}
	if sk.signal == "" {
		t.Error("signal should be built for matching")
	}
}

// TestPlainMarkdownAutoMatch is the core "just write normal markdown" feature:
// a doc dropped directly under skills/ with NO frontmatter, NO triggers, and NO
// subfolder must be discovered by a relevant question via content matching.
func TestPlainMarkdownAutoMatch(t *testing.T) {
	dir := t.TempDir()
	// A totally plain doc: a heading + a bullet list, filename in natural
	// language. Exactly what a non-technical user would write on their phone.
	doc := "# 应对老板画饼\n\n- 老板承诺升职加薪但迟迟不兑现\n- 先把承诺落到书面\n- 观察兑现窗口\n"
	if err := os.WriteFile(filepath.Join(dir, "如何应对老板画饼.md"), []byte(doc), 0644); err != nil {
		t.Fatal(err)
	}
	m, err := NewMatcherWithDir(dir, nil)
	if err != nil {
		t.Fatal(err)
	}

	// A question sharing the topic should surface the doc.
	got := m.Match("老板又画饼了怎么办")
	found := false
	for _, s := range got {
		if s.Name == "如何应对老板画饼" {
			found = true
			if !strings.Contains(s.Content, "书面") {
				t.Error("matched skill should carry its full body")
			}
		}
	}
	if !found {
		t.Fatalf("plain markdown doc was not auto-matched; got %d matches: %+v", len(got), got)
	}

	// An unrelated question must NOT surface it (guard against match-everything).
	if un := m.Match("今天天气不错适合散步"); len(un) != 0 {
		t.Errorf("unrelated question should not match the doc, got %+v", un)
	}
}

// TestDeepNestingAndEnglishNames verifies docs at arbitrary depth with English
// filenames are supported, and same-basename files in different folders get
// unique names (path-based).
func TestDeepNestingAndEnglishNames(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, body string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// Deep English doc, no frontmatter.
	mustWrite("career/2026/my-plan.md", "# My job change plan\n\n- target senior role\n- update resume\n")
	// Same basename in a different folder → must NOT collide.
	mustWrite("life/my-plan.md", "# Weekend fitness plan\n\n- run 5k\n")

	m, err := NewMatcherWithDir(dir, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Names are path-based and therefore unique.
	names := map[string]bool{}
	for _, s := range m.List() {
		names[s.Name] = true
	}
	if !names["career/2026/my-plan"] || !names["life/my-plan"] {
		t.Fatalf("expected path-based unique names, got %v", names)
	}

	// English question matches the deep English doc via the shared word "resume"/"plan".
	got := m.Match("help me update my resume for the job change")
	found := false
	for _, s := range got {
		if s.Name == "career/2026/my-plan" {
			found = true
		}
	}
	if !found {
		t.Fatalf("deep English doc not matched; got %+v", got)
	}
}

func TestNewMatcherWithDir(t *testing.T) {
	dir := t.TempDir()
	m, err := NewMatcherWithDir(dir, nil)
	if err != nil {
		t.Fatalf("NewMatcherWithDir: %v", err)
	}
	if m.SkillDir() != dir {
		t.Errorf("SkillDir = %q, want %q", m.SkillDir(), dir)
	}
	// No embedded skills — dir starts empty, skills are added by the importer on first boot.
	// The matcher is valid with zero skills loaded.
	t.Logf("skills loaded: %d (expected 0 on fresh dir)", len(m.skills))
}

func TestNewMatcherWithDir_MkdirError(t *testing.T) {
	// A path under an existing regular file cannot be created as a dir.
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewMatcherWithDir(filepath.Join(f, "sub"), nil); err == nil {
		t.Error("NewMatcherWithDir should fail when dir cannot be created")
	}
}

func TestSaveGetDelete(t *testing.T) {
	dir := t.TempDir()
	m, err := NewMatcherWithDir(dir, nil)
	if err != nil {
		t.Fatal(err)
	}

	def := agent.SkillDef{
		Name:        "unit-test-skill",
		Description: "created by test",
		Triggers:    []string{"zzztrigger"},
		Deps:        []string{"dep-a"},
		Content:     "Body of the skill.",
	}
	if err := m.Save(def); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Get should return the saved skill.
	got, ok := m.Get("unit-test-skill")
	if !ok {
		t.Fatal("Get should find saved skill")
	}
	if got.Description != "created by test" || len(got.Triggers) != 1 || got.Triggers[0] != "zzztrigger" {
		t.Errorf("saved skill mismatch: %+v", got)
	}
	if len(got.Deps) != 1 || got.Deps[0] != "dep-a" {
		t.Errorf("deps mismatch: %+v", got.Deps)
	}

	// Match on its trigger.
	if res := m.Match("please zzztrigger now"); len(res) == 0 {
		t.Error("Match should find the saved skill by trigger")
	}

	// It appears in List.
	found := false
	for _, d := range m.List() {
		if d.Name == "unit-test-skill" {
			found = true
		}
	}
	if !found {
		t.Error("List should include saved skill")
	}

	// Delete removes it.
	if err := m.Delete("unit-test-skill"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := m.Get("unit-test-skill"); ok {
		t.Error("Get should not find deleted skill")
	}
}

func TestSave_ReadOnlyMatcher(t *testing.T) {
	m, err := NewMatcher() // embedded-only, no dir
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Save(agent.SkillDef{Name: "x", Triggers: []string{"t"}}); err == nil {
		t.Error("Save on read-only matcher should error")
	}
	if err := m.Delete("x"); err == nil {
		t.Error("Delete on read-only matcher should error")
	}
}

func TestSave_EmptyName(t *testing.T) {
	m, err := NewMatcherWithDir(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Save(agent.SkillDef{Name: ""}); err == nil {
		t.Error("Save should require a name")
	}
}

func TestSave_NoTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	m, err := NewMatcherWithDir(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Content already ends with newline: exercises the else branch in renderSkill.
	if err := m.Save(agent.SkillDef{
		Name:     "nl-skill",
		Triggers: []string{"nltrig"},
		Content:  "ends with newline\n",
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "skill", "nl-skill.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "newline\n") {
		t.Errorf("unexpected file tail: %q", string(data))
	}
}

func TestDelete_NotFound(t *testing.T) {
	m, err := NewMatcherWithDir(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Delete("does-not-exist"); err == nil {
		t.Error("Delete of missing skill should error")
	}
}

func TestReload(t *testing.T) {
	dir := t.TempDir()
	m, err := NewMatcherWithDir(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := len(m.List())

	// Write a new skill file directly to disk, then Reload.
	doc := "---\nname: reloaded\ntriggers: [reloadtrig]\n---\n\nHi\n"
	if err := os.WriteFile(filepath.Join(dir, "reloaded.md"), []byte(doc), 0644); err != nil {
		t.Fatal(err)
	}
	if err := m.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(m.List()) != before+1 {
		t.Errorf("after Reload expected %d skills, got %d", before+1, len(m.List()))
	}
	if _, ok := m.Get("reloaded"); !ok {
		t.Error("Reload should pick up new file")
	}
}

func TestReadDoc(t *testing.T) {
	dir := t.TempDir()
	m, err := NewMatcherWithDir(dir, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Write a runtime doc and read it back (runtime-dir path preferred).
	rel := "notes/hello.md"
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("runtime content"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := m.ReadDoc(rel)
	if err != nil {
		t.Fatalf("ReadDoc: %v", err)
	}
	if got != "runtime content" {
		t.Errorf("ReadDoc = %q", got)
	}
}

func TestReadDoc_PathTraversal(t *testing.T) {
	m, err := NewMatcherWithDir(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../secret", "../../etc/passwd", "..", ""} {
		if _, err := m.ReadDoc(bad); err == nil {
			t.Errorf("ReadDoc(%q) should be rejected", bad)
		}
	}
}

func TestReadDoc_NotFound(t *testing.T) {
	m, err := NewMatcherWithDir(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReadDoc("no/such/file.md"); err == nil {
		t.Error("ReadDoc of missing file should error")
	}
}

func TestListDocs(t *testing.T) {
	dir := t.TempDir()
	m, err := NewMatcherWithDir(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Add a runtime-only file.
	if err := os.WriteFile(filepath.Join(dir, "runtime-only.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	docs := m.ListDocs()
	if len(docs) == 0 {
		t.Fatal("ListDocs returned nothing")
	}
	// Runtime file present.
	if !contains(docs, "runtime-only.md") {
		t.Errorf("ListDocs missing runtime file; got %v", docs)
	}
	// No duplicates (embedded files were copied to disk, must be de-duped).
	seen := map[string]bool{}
	for _, d := range docs {
		if seen[d] {
			t.Errorf("duplicate doc in ListDocs: %q", d)
		}
		seen[d] = true
	}
}

func TestIsSkillFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"skills/workplace/advisor.md", true},
		{"skills/resume/SKILL.md", true},
		{"inside/people/A.md", true}, // all categories matchable alike
		{"rules/rules.md", true},     // ditto
		{"memories/note.md", true},   // ditto
		{"my-plan.md", true},         // plain root-level doc
		{"a/b/c/deep-note.md", true}, // any depth
		{"matcher.go", false},
		{"data.json", false},
		{"skills/AGENT-INSTALL.md", false}, // reserved tooling doc
		{"AGENT-INSTALL.md", false},
	}
	for _, tt := range tests {
		if got := isSkillFile(tt.path); got != tt.want {
			t.Errorf("isSkillFile(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestParseSkill_NoFrontmatter(t *testing.T) {
	// A plain markdown doc with no frontmatter is now a VALID skill: its name
	// defaults to the filename and it is matched automatically. This is the core
	// "just write normal markdown" feature.
	sk := parseSkill("# 如何应对老板画饼\n\n遇到老板承诺不兑现时……", "如何应对老板画饼.md")
	if sk.Name != "如何应对老板画饼" {
		t.Errorf("name = %q, want filename-derived name", sk.Name)
	}
	if len(sk.Triggers) != 0 {
		t.Errorf("expected no explicit triggers, got %v", sk.Triggers)
	}
	// On-demand retrieval: Content is cleared at parse time (lazy load); the
	// signal stays for auto-matching.
	if sk.Content != "" {
		t.Error("content should be lazily loaded (empty at parse)")
	}
	if sk.signal == "" {
		t.Error("signal should be built for auto-matching")
	}
}

func TestParseSkill_MalformedLines(t *testing.T) {
	raw := "---\n" +
		"name: has-name\n" +
		"\n" + // blank line skipped
		"noColonLine\n" + // no colon → skipped
		"unknownkey: value\n" + // unknown key → ignored
		"deps: [d1, d2]\n" +
		"---\n\nbody"
	sk := parseSkill(raw, "has-name.md")
	if sk.Name != "has-name" {
		t.Errorf("name = %q", sk.Name)
	}
	if len(sk.Deps) != 2 {
		t.Errorf("deps = %v", sk.Deps)
	}
}

func TestMatch_Concurrent(t *testing.T) {
	m := newTestMatcher(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); m.Match("冲突") }()
	}
	wg.Wait()
}

func contains(ss []string, target string) bool {
	for _, s := range ss {
		if s == target {
			return true
		}
	}
	return false
}

// TestSave_WriteError exercises Save's WriteFile error path by making the
// runtime dir read-only after the matcher is constructed.
func TestSave_WriteError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses file permissions")
	}
	dir := t.TempDir()
	m, err := NewMatcherWithDir(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0755) })
	err = m.Save(agent.SkillDef{Name: "wontwrite", Triggers: []string{"t"}, Content: "x"})
	if err == nil {
		t.Error("Save should fail writing into a read-only dir")
	}
}

// TestReload_UnreadableFileSkipped exercises reload's os.ReadFile error branch
// (an unreadable skill file is silently skipped rather than aborting reload).
func TestReload_UnreadableFileSkipped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses file permissions")
	}
	dir := t.TempDir()
	m, err := NewMatcherWithDir(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "unreadable.md")
	if err := os.WriteFile(bad, []byte("---\nname: x\ntriggers: [t]\n---\nbody"), 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(bad, 0644) })
	if err := m.Reload(); err != nil {
		t.Errorf("Reload should skip unreadable files, got %v", err)
	}
}

// Runtime-generated views (memory/task/star/calendar/consult) must not enter
// the skill pool — before this guard they were parsed as skills on reload and
// crowded real skills out of the top-N match.
func TestReloadExcludesRuntimeDataDirs(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("skill/real.md", "---\nname: real-skill\ndescription: d\ntriggers: [职场]\n---\n\nframework")
	write("memory/2026-08.md", "# 记忆 — 2026-08\n\n- some extracted fact")
	write("task/2026-08.md", "- [ ] a task")
	write("star/2026-08.md", "star report")
	write("inside/person.md", "# 张三\n\nprofile text")

	m, err := NewMatcherWithDir(dir, nil)
	if err != nil {
		t.Fatalf("NewMatcherWithDir: %v", err)
	}
	names := map[string]bool{}
	for _, s := range m.List() {
		names[s.Name] = true
	}
	if !names["real-skill"] {
		t.Errorf("real skill missing from pool: %v", names)
	}
	for bad := range names {
		if strings.HasPrefix(bad, "memory/") || strings.HasPrefix(bad, "task/") ||
			strings.HasPrefix(bad, "star/") || strings.HasPrefix(bad, "calendar/") ||
			strings.HasPrefix(bad, "consult/") {
			t.Errorf("runtime data dir leaked into skill pool: %q", bad)
		}
	}
	// inside/ is a knowledge category and stays matchable (frontmatter-less
	// docs are named by their path).
	if !names["inside/person"] {
		t.Errorf("inside person doc should remain matchable: %v", names)
	}
}

func TestIsSkillFileRuntimeDirs(t *testing.T) {
	cases := map[string]bool{
		"skill/sub/x.md":      true,
		"inside/deep/p.md":    true,
		"rule/r.md":           true,
		"memory/2026-08.md":   false,
		"task/2026-08.md":     false,
		"star/2026-08.md":     false,
		"calendar/2026-08.md": false,
		"consult/2026-08.md":  false,
		"agent-install.md":    false,
		"memory/notes.txt":    false, // not .md either
	}
	for path, want := range cases {
		if got := isSkillFile(path); got != want {
			t.Errorf("isSkillFile(%q) = %v, want %v", path, got, want)
		}
	}
}
