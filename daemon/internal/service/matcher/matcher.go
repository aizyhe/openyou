package matcher

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"openyou/daemon/internal/ai/agent"
)

// skillFiles is intentionally empty — all skill documents are provided by the
// caller via NewMatcherWithDir's embedded parameter (typically importer.SeedFS).
var skillFiles fs.FS = emptyFS{}

type LoadedSkill struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Triggers    []string `json:"triggers"`
	Deps        []string `json:"deps"`
	Content     string   `json:"content"`

	// path excludes dependency files from standalone matching — they're injected
	// via their parent skill's deps, not matched on their own.
	path string
	// signal is the matching surface WITHOUT explicit triggers: filename + every
	// markdown heading, kept small so content auto-matching stays low-noise.
	signal string
}

type Matcher struct {
	mu       sync.RWMutex
	skills   []LoadedSkill
	dir      string // runtime dir for user edits; empty = none
	embedded fs.FS  // embedded skill docs; nil = none

	// Incremental refresh: on-disk edits are detected by file fingerprints
	// (size+mtime, metadata only) and only the changed docs re-scanned.
	scan     map[string]fileStamp
	lastScan time.Time
}

// fileStamp is a cheap metadata-only fingerprint (size+mtime) for change detection.
type fileStamp struct {
	size  int64
	mtime int64
}

// docChunk is one retrievable unit of a document: a "##"-level section, or the
// whole doc when it has none. Chunks keep the budget wide enough for several
// sources and stop one big file from monopolizing the prompt.
type docChunk struct {
	path  string // doc relative path (source attribution)
	title string // heading chain, e.g. "## 09 · Salary negotiation > ### Q3"
	text  string // chunk body
}

func NewMatcher() (*Matcher, error) {
	m := &Matcher{}
	return m, m.loadEmbedded()
}

// NewMatcherWithDir loads skills from the embedded FS plus a runtime dir, where
// same-path files override embedded ones so users can edit skills without rebuilding.
func NewMatcherWithDir(dir string, embedded fs.FS) (*Matcher, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	// Seed .md files from the embedded FS into the runtime dir; never overwrite
	// existing files so user edits survive.
	var seeded int
	if embedded != nil {
		_ = fs.WalkDir(embedded, "userdata", func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel := strings.TrimPrefix(path, "userdata/")
			if rel == path {
				return nil
			}
			if !strings.HasSuffix(strings.ToLower(rel), ".md") &&
				!isConfigSeed(rel) {
				return nil
			}
			dst := filepath.Join(dir, filepath.FromSlash(rel))
			if _, statErr := os.Stat(dst); statErr == nil {
				return nil
			}
			data, readErr := fs.ReadFile(embedded, path)
			if readErr != nil {
				return nil
			}
			if mkdirErr := os.MkdirAll(filepath.Dir(dst), 0755); mkdirErr != nil {
				return nil
			}
			if writeErr := os.WriteFile(dst, data, 0644); writeErr != nil {
				log.Printf("matcher: seed write %s: %v", path, writeErr)
				return nil
			}
			seeded++
			return nil
		})
	}
	log.Printf("matcher: seeded %d .md files from embedded into %s", seeded, dir)
	m := &Matcher{dir: dir, embedded: embedded}
	return m, m.reload()
}

func (m *Matcher) loadEmbedded() error {
	var skills []LoadedSkill
	err := fs.WalkDir(skillFiles, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !isSkillFile(path) {
			return nil
		}
		data, readErr := fs.ReadFile(skillFiles, path)
		if readErr != nil {
			return nil
		}
		sk := parseSkill(string(data), path)
		if sk.Name == "" {
			return nil
		}
		skills = append(skills, sk)
		return nil
	})
	if err != nil {
		return err
	}
	m.skills = skills
	return nil
}

func (m *Matcher) reload() error {
	seen := make(map[string]bool)
	var skills []LoadedSkill

	// 1. Embedded FS (base — from //go:embed in the importer package)
	if m.embedded != nil {
		_ = fs.WalkDir(m.embedded, "userdata", func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel := strings.TrimPrefix(path, "userdata/")
			if rel == path {
				return nil
			}
			if !isSkillFile(rel) {
				return nil
			}
			data, readErr := fs.ReadFile(m.embedded, path)
			if readErr != nil {
				return nil
			}
			sk := parseSkill(string(data), rel)
			if sk.Name == "" {
				return nil
			}
			skills = append(skills, sk)
			seen[rel] = true
			return nil
		})
	}

	// 2. Runtime dir (user edits — override embedded entries with same path)
	if m.dir != "" {
		_ = filepath.WalkDir(m.dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, relErr := filepath.Rel(m.dir, path)
			if relErr != nil {
				rel = path
			}
			rel = filepath.ToSlash(rel)
			if !isSkillFile(rel) {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}
			sk := parseSkill(string(data), rel)
			if sk.Name == "" {
				return nil
			}
			if seen[rel] {
				for i, s := range skills {
					if s.path == rel {
						skills[i] = sk
						break
					}
				}
			} else {
				skills = append(skills, sk)
				seen[rel] = true
			}
			return nil
		})
	}

	m.mu.Lock()
	m.skills = skills
	m.scan = scanRuntimeDir(m.dir)
	m.lastScan = time.Now()
	m.mu.Unlock()
	return nil
}

// runtimeDataDirs are top-level userdata dirs holding runtime-generated views
// (memory journals, task lists) rather than curated knowledge — excluded so
// ever-growing files don't crowd real skills out of top-N.
var runtimeDataDirs = map[string]bool{
	"memory": true, "task": true, "star": true, "calendar": true, "consult": true,
}

// isConfigSeed reports whether a path is the user's LLM config file (openyou.json/.jsonc).
func isConfigSeed(path string) bool {
	slash := filepath.ToSlash(path)
	base := slash
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	base = strings.ToLower(base)
	return base == "openyou.json" || base == "openyou.jsonc"
}

// isSkillFile reports whether a path is matchable: any .md under the knowledge
// dirs (inside/rule/skill, any depth) except AGENT-INSTALL.md and runtime data dirs.
func isSkillFile(path string) bool {
	if !strings.HasSuffix(strings.ToLower(path), ".md") {
		return false
	}
	slash := filepath.ToSlash(path)
	if i := strings.IndexByte(slash, '/'); i >= 0 {
		if runtimeDataDirs[slash[:i]] {
			return false
		}
	}
	base := slash
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	if strings.EqualFold(base, "agent-install.md") {
		return false
	}
	return true
}

func (m *Matcher) Reload() error { return m.reload() }

// refreshMinInterval throttles change detection so hot paths (List polling,
// per-message retrieval) scan the tree at most once per interval.
const refreshMinInterval = 1 * time.Second

// scanRuntimeDir fingerprints matchable docs by (size, mtime) — metadata only.
func scanRuntimeDir(dir string) map[string]fileStamp {
	out := make(map[string]fileStamp)
	if dir == "" {
		return out
	}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !isSkillFile(rel) {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		out[rel] = fileStamp{size: info.Size(), mtime: info.ModTime().UnixNano()}
		return nil
	})
	return out
}

// refreshIfChanged re-parses only files added/changed/deleted on disk (e.g. a
// desktop sync); unchanged docs cost a metadata walk.
func (m *Matcher) refreshIfChanged() {
	if m.dir == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if time.Since(m.lastScan) < refreshMinInterval {
		return
	}
	m.lastScan = time.Now()

	cur := scanRuntimeDir(m.dir)
	var changed []string
	for rel, st := range cur {
		if old, ok := m.scan[rel]; !ok || old != st {
			changed = append(changed, rel)
		}
	}
	for rel := range m.scan {
		if _, ok := cur[rel]; !ok {
			changed = append(changed, rel)
		}
	}
	if len(changed) == 0 {
		m.scan = cur
		return
	}

	// Re-parse only the affected files.
	for _, rel := range changed {
		if _, exists := cur[rel]; !exists {
			m.removeSkill(rel)
			continue
		}
		data, err := os.ReadFile(filepath.Join(m.dir, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		sk := parseSkill(string(data), rel)
		if sk.Name == "" {
			continue
		}
		m.upsertSkill(sk, rel)
	}
	m.scan = cur
}

// RefreshDoc incrementally re-parses one runtime doc immediately, so a one-file
// edit never pays for a full rescan; deleting a doc with an embedded default
// restores that default.
func (m *Matcher) RefreshDoc(rel string) error {
	if m.dir == "" {
		return fmt.Errorf("skills: read-only matcher has no runtime docs")
	}
	rel = filepath.ToSlash(filepath.Clean("/" + rel))
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("skills: invalid doc path %q", rel)
	}
	// Time-class dirs (memory/task/star/calendar/consult) hold runtime-generated
	// views, not matchable knowledge — saving still succeeds, but the file never
	// enters the skill pool.
	if !isSkillFile(rel) {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	full := filepath.Join(m.dir, filepath.FromSlash(rel))
	if _, err := os.Stat(full); err == nil {
		data, rerr := os.ReadFile(full)
		if rerr != nil {
			return rerr
		}
		m.upsertSkill(parseSkill(string(data), rel), rel)
		if info, ierr := os.Stat(full); ierr == nil {
			m.scan[rel] = fileStamp{size: info.Size(), mtime: info.ModTime().UnixNano()}
		}
	} else {
		m.removeSkill(rel)
		delete(m.scan, rel)
	}
	m.lastScan = time.Now()
	return nil
}

// upsertSkill replaces the doc at rel, or appends it when new.
func (m *Matcher) upsertSkill(sk LoadedSkill, rel string) {
	for i := range m.skills {
		if m.skills[i].path == rel {
			m.skills[i] = sk
			return
		}
	}
	m.skills = append(m.skills, sk)
}

// removeSkill drops the doc at rel; an embedded copy for the path is restored.
func (m *Matcher) removeSkill(rel string) {
	for i := range m.skills {
		if m.skills[i].path != rel {
			continue
		}
		m.skills = append(m.skills[:i], m.skills[i+1:]...)
		break
	}
	if m.embedded == nil {
		return
	}
	if data, err := fs.ReadFile(m.embedded, "userdata/"+rel); err == nil {
		sk := parseSkill(string(data), rel)
		if sk.Name != "" {
			m.skills = append(m.skills, sk)
		}
	}
}

func (m *Matcher) SkillDir() string { return m.dir }

func (m *Matcher) ListDocs() []string {
	m.refreshIfChanged()
	var out []string
	seen := make(map[string]bool)
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if m.dir != "" {
		_ = filepath.WalkDir(m.dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if rel, e := filepath.Rel(m.dir, path); e == nil {
				add(filepath.ToSlash(rel))
			}
			return nil
		})
	}
	if m.embedded != nil {
		_ = fs.WalkDir(m.embedded, "userdata", func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel := strings.TrimPrefix(path, "userdata/")
			if rel != path {
				add(rel)
			}
			return nil
		})
	}
	return out
}

// ReadDoc returns a doc's raw contents by relative path, runtime dir (user edits)
// preferred, falling back to the embedded default.
func (m *Matcher) ReadDoc(relpath string) (string, error) {
	clean := filepath.ToSlash(filepath.Clean("/" + relpath))
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("skills: invalid doc path %q", relpath)
	}
	if m.dir != "" {
		if data, err := os.ReadFile(filepath.Join(m.dir, clean)); err == nil {
			return string(data), nil
		}
	}
	if m.embedded != nil {
		if data, err := fs.ReadFile(m.embedded, "userdata/"+clean); err == nil {
			return string(data), nil
		}
	}
	return "", fmt.Errorf("skills: doc %q not found", relpath)
}

var timeClassDirs = map[string]bool{
	"task": true, "star": true, "calendar": true, "memory": true,
}

func validMonthName(name string) bool {
	base := strings.TrimSuffix(name, ".md")
	if base == name {
		return false // no .md suffix
	}
	if len(base) != 7 || base[4] != '-' {
		return false
	}
	for i, r := range []rune(base) {
		if i == 4 {
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ReadDir concatenates all .md files under a top-level userdata dir (sorted by
// filename); time-class dirs only include "YYYY-MM.md" files. Missing dir → ("", nil).
func (m *Matcher) ReadDir(dir string) (string, error) {
	var b strings.Builder
	entries, err := m.listDir(dir)
	if err != nil {
		return "", nil
	}
	for _, path := range entries {
		if data, err := m.ReadDoc(path); err == nil && strings.TrimSpace(data) != "" {
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(strings.TrimSpace(data))
		}
	}
	return b.String(), nil
}

// docRetrievalTopK caps how many docs are opened for fine-grained chunk scoring.
const docRetrievalTopK = 30

// RelevantDocs returns the inside/ sections most relevant to a query within a
// rune budget — two-stage: coarse in-memory doc-level scoring, then lazy
// chunk-level fine scoring that injects matching "##" sections, never whole docs.
func (m *Matcher) RelevantDocs(query string, budget int) []string {
	if budget <= 0 || strings.TrimSpace(query) == "" {
		return nil
	}
	m.refreshIfChanged()
	m.mu.RLock()
	defer m.mu.RUnlock()

	qTokens := queryTokens(query)
	if len(qTokens) == 0 {
		return nil
	}

	type scoredDoc struct {
		path  string
		score int
	}
	var cand []scoredDoc
	for _, sk := range m.skills {
		if !strings.HasPrefix(sk.path, "inside/") {
			continue
		}
		s := countTokenHits(signalTokens(sk.signal), qTokens)
		if s == 0 {
			continue // no token overlap — not relevant to this query
		}
		cand = append(cand, scoredDoc{path: sk.path, score: s})
	}
	if len(cand) == 0 {
		return nil
	}
	sort.SliceStable(cand, func(i, j int) bool {
		if cand[i].score != cand[j].score {
			return cand[i].score > cand[j].score
		}
		return cand[i].path < cand[j].path
	})
	if len(cand) > docRetrievalTopK {
		cand = cand[:docRetrievalTopK]
	}

	type scoredChunk struct {
		title string
		text  string
		score int
	}
	var chunks []scoredChunk
	for _, d := range cand {
		data, err := m.ReadDoc(d.path)
		if err != nil || strings.TrimSpace(data) == "" {
			continue
		}
		for _, c := range splitChunks(d.path, data) {
			head := strings.TrimSpace(c.title)
			if head == "" {
				head = d.path
			}
			s := countTokenHits(signalTokens(strings.ToLower(head+"\n"+c.text)), qTokens)
			if s == 0 {
				continue
			}
			chunks = append(chunks, scoredChunk{title: head, text: c.text, score: s})
		}
	}
	if len(chunks) == 0 {
		return nil
	}
	sort.SliceStable(chunks, func(i, j int) bool {
		if chunks[i].score != chunks[j].score {
			return chunks[i].score > chunks[j].score
		}
		return chunks[i].title < chunks[j].title
	})

	// Budgeted injection of complete chunks (never mid-sentence).
	var out []string
	remaining := budget
	for _, c := range chunks {
		if remaining <= 0 {
			break
		}
		full := "### " + c.title + "\n\n" + strings.TrimSpace(c.text)
		if rc := utf8.RuneCountInString(full); rc > remaining {
			// Chunk that won't fit: take a whole-line tail, or skip it entirely.
			if cut := tailToFit(c.text, remaining); cut != "" {
				full = "### " + c.title + "\n\n" + cut + "\n…(truncated)"
			} else {
				continue
			}
		}
		remaining -= utf8.RuneCountInString(full)
		out = append(out, full)
	}
	return out
}

// tailToFit keeps whole lines within max runes so a chunk is truncated at a line
// boundary, never mid-sentence; an over-long first line yields "" for skipping.
func tailToFit(text string, max int) string {
	if max <= 0 {
		return ""
	}
	var keep []string
	n := 0
	for _, ln := range strings.Split(text, "\n") {
		lc := utf8.RuneCountInString(ln)
		if n+lc > max {
			break
		}
		keep = append(keep, ln)
		n += lc + 1
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

// queryTokens splits a query into retrieval tokens: latin words (3+ chars,
// stopword-filtered) and Han bigrams — not single runes, so common characters
// like 的/我/是 don't drown real signal.
func queryTokens(q string) []string {
	lower := strings.ToLower(q)
	seen := make(map[string]bool)
	var out []string
	for _, w := range latinWordRe.FindAllString(lower, -1) {
		if !stopWord(w) && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	for g := range hanBigrams(lower) {
		if !seen[g] && usefulBigram(g) {
			seen[g] = true
			out = append(out, g)
		}
	}
	return out
}

func signalTokens(signal string) map[string]bool {
	toks := make(map[string]bool)
	for _, w := range latinWordRe.FindAllString(signal, -1) {
		toks[w] = true
	}
	for g := range hanBigrams(signal) {
		if usefulBigram(g) {
			toks[g] = true
		}
	}
	return toks
}

// countTokenHits scores a token set against query tokens; latin words weigh 3, Han bigrams 1.
func countTokenHits(toks map[string]bool, qTokens []string) int {
	score := 0
	for _, t := range qTokens {
		if toks[t] {
			if isLatinWord(t) {
				score += 3
			} else {
				score += 1
			}
		}
	}
	return score
}

func isLatinWord(t string) bool {
	for _, r := range t {
		if r > 0x7F {
			return false
		}
	}
	return len(t) > 0
}

// hanStopRunes are high-frequency Han runes carrying no retrieval signal.
var hanStopRunes = "的了我是你在有和就不这那一也都很会能要上下中大"

func usefulBigram(g string) bool {
	for _, r := range g {
		if strings.ContainsRune(hanStopRunes, r) {
			return false
		}
	}
	return true
}

var latinWordRe = regexp.MustCompile(`[a-z]{3,}`)

// splitChunks splits a document into "##"-level chunks: every heading is a
// boundary, the first level-1 heading becomes a title chain prefix, and
// frontmatter is stripped first. A doc with no headings stays one chunk.
func splitChunks(path, content string) []docChunk {
	body := content
	if _, rest, ok := splitFrontmatter(content); ok {
		body = rest
	}
	loc := headingRe.FindAllStringSubmatchIndex(body, -1)
	if len(loc) == 0 {
		return []docChunk{{path: path, title: baseFromPath(path), text: strings.TrimSpace(body)}}
	}

	var docTitle string
	for _, l := range loc {
		if countHeadingLevel(body[l[0]:l[2]]) == 1 {
			docTitle = strings.TrimSpace(body[l[2]:l[3]])
			break
		}
	}

	var out []docChunk
	for i, l := range loc {
		title := strings.TrimSpace(body[l[2]:l[3]])
		start := l[1]
		end := len(body)
		if i+1 < len(loc) {
			end = loc[i+1][0]
		}
		text := strings.TrimSpace(body[start:end])
		if text == "" {
			continue // empty section — nothing retrievable
		}
		chain := title
		if docTitle != "" && docTitle != title {
			chain = docTitle + " > " + title
		}
		out = append(out, docChunk{path: path, title: chain, text: text})
	}
	if len(out) == 0 {
		// Headings existed but every section was empty — whole body as one chunk.
		return []docChunk{{path: path, title: baseFromPath(path), text: strings.TrimSpace(body)}}
	}
	return out
}

func countHeadingLevel(marker string) int {
	n := 0
	for _, r := range marker {
		if r == '#' {
			n++
		} else {
			break
		}
	}
	return n
}

func stopWord(w string) bool {
	switch w {
	case "the", "and", "for", "with", "you", "your", "how", "what", "why", "this",
		"that", "from", "have", "about", "interview":
		return true
	}
	return false
}

// listDir returns sorted .md paths under a top-level dir, runtime first then embedded.
func (m *Matcher) listDir(dir string) ([]string, error) {
	timeClass := timeClassDirs[dir]
	var out []string
	seen := map[string]bool{}
	var collect func(base, prefix string)
	collect = func(base, prefix string) {
		entries, err := os.ReadDir(base)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() {
				if timeClass {
					continue // time-class dirs stay flat (YYYY-MM.md)
				}
				collect(filepath.Join(base, e.Name()), prefix+"/"+e.Name())
				continue
			}
			if !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
				continue
			}
			if timeClass && !validMonthName(e.Name()) {
				continue
			}
			rel := prefix + "/" + e.Name()
			if !seen[rel] {
				seen[rel] = true
				out = append(out, rel)
			}
		}
	}
	if m.dir != "" {
		collect(filepath.Join(m.dir, dir), dir)
	}
	if m.embedded != nil {
		walkEmbedded(m.embedded, dir, timeClass, seen, &out)
	}
	sort.Strings(out)
	return out, nil
}

func walkEmbedded(fsys fs.FS, dir string, timeClass bool, seen map[string]bool, out *[]string) {
	entries, err := fs.ReadDir(fsys, "userdata/"+dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			if timeClass {
				continue
			}
			walkEmbedded(fsys, dir+"/"+e.Name(), timeClass, seen, out)
			continue
		}
		if !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		if timeClass && !validMonthName(e.Name()) {
			continue
		}
		rel := dir + "/" + e.Name()
		if !seen[rel] {
			seen[rel] = true
			*out = append(*out, rel)
		}
	}
}

// maxMatchedSkills caps returned matches at the brain trust's sub-agent count.
const maxMatchedSkills = 5

// Match returns matched skills sorted by strength, capped at maxMatchedSkills —
// the most relevant skills win brain-trust slots, not whichever files loaded first.
func (m *Matcher) Match(userMessage string) []agent.SkillInfo {
	m.refreshIfChanged()
	m.mu.RLock()
	defer m.mu.RUnlock()
	lower := strings.ToLower(userMessage)

	// Deps declared by another skill must not match standalone — collect them once.
	depOf := make(map[string]bool)
	for _, sk := range m.skills {
		for _, d := range sk.Deps {
			depOf[normalizeDocPath(d)] = true
		}
	}

	var scored []scoredSkill
	for _, sk := range m.skills {
		if depOf[normalizeDocPath(sk.path)] {
			continue
		}
		var score int
		if len(sk.Triggers) > 0 {
			score = triggerScore(lower, sk.Triggers)
		} else {
			score = contentScore(lower, sk.signal)
		}
		if score > 0 {
			scored = append(scored, scoredSkill{
				info: agent.SkillInfo{
					Name: sk.Name, Description: sk.Description, Deps: sk.Deps,
					Path: sk.path, Content: "",
				},
				score: score,
			})
		}
	}

	sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	if len(scored) > maxMatchedSkills {
		scored = scored[:maxMatchedSkills]
	}
	result := make([]agent.SkillInfo, len(scored))
	for i, s := range scored {
		result[i] = s.info
		// Lazy load: only matched top-N skills have their full content read in.
		if c, err := m.ReadDoc(s.info.Path); err == nil && strings.TrimSpace(c) != "" {
			result[i].Content = strings.TrimSpace(c)
		}
	}
	return result
}

type scoredSkill struct {
	info  agent.SkillInfo
	score int
}

func triggerScore(msg string, triggers []string) int {
	score := 0
	for _, t := range triggers {
		if strings.Contains(msg, strings.ToLower(t)) {
			score++
		}
	}
	return score
}

func contentScore(msgLower, signalLower string) int {
	if signalLower == "" {
		return 0
	}
	score := 0
	sigTokens := make(map[string]bool)
	for _, t := range tokenize(signalLower) {
		if len(t) >= 2 {
			sigTokens[t] = true
		}
	}
	for _, t := range tokenize(msgLower) {
		if len(t) >= 2 && sigTokens[t] {
			score += 2
		}
	}
	score += sharedHanBigrams(msgLower, signalLower)
	return score
}

// parseSkill parses a skill document; frontmatter is optional (a plain .md is a
// valid skill) and can override name/description or set triggers/deps.
func parseSkill(raw, path string) LoadedSkill {
	var sk LoadedSkill
	sk.path = path
	body := raw

	// A leading frontmatter block is optional; anything else is pure content.
	if fm, rest, ok := splitFrontmatter(raw); ok {
		body = rest
		for _, line := range strings.Split(fm, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			idx := strings.Index(line, ":")
			if idx < 0 {
				continue
			}
			key := strings.TrimSpace(line[:idx])
			val := strings.TrimSpace(line[idx+1:])
			switch key {
			case "name":
				sk.Name = val
			case "description":
				sk.Description = val
			case "triggers":
				sk.Triggers = parseTriggerArray(val)
			case "deps":
				sk.Deps = parseTriggerArray(val)
			}
		}
	}

	sk.Content = strings.TrimSpace(body)
	if sk.Name == "" {
		sk.Name = nameFromPath(path)
	}
	sk.signal = buildSignal(path, sk.Content)

	// Content is never held resident: only the lightweight signal stays in memory,
	// the full doc is read on demand when Match/Get hits.
	sk.Content = ""
	return sk
}

// splitFrontmatter requires the fence to be the very first line, so a "---" rule
// in prose isn't misread as a fence.
func splitFrontmatter(raw string) (fm, rest string, ok bool) {
	s := strings.TrimPrefix(raw, "\ufeff") // strip any UTF-8 BOM
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return "", "", false
	}
	nl := strings.IndexByte(s, '\n')
	afterOpen := s[nl+1:]
	for _, sep := range []string{"\n---\n", "\n---\r\n"} {
		if idx := strings.Index(afterOpen, sep); idx >= 0 {
			return afterOpen[:idx], afterOpen[idx+len(sep):], true
		}
	}
	if strings.HasSuffix(afterOpen, "\n---") {
		return strings.TrimSuffix(afterOpen, "\n---"), "", true
	}
	return "", "", false
}

// nameFromPath derives a unique skill name from its full relative path (minus .md),
// so same-basename files in different folders don't collide.
func nameFromPath(path string) string {
	p := filepath.ToSlash(path)
	return strings.TrimSuffix(p, ".md")
}

func baseFromPath(path string) string {
	base := filepath.ToSlash(path)
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	return strings.TrimSuffix(base, ".md")
}

var headingRe = regexp.MustCompile(`(?m)^#{1,6}\s+(.+?)\s*$`)

// maxHeadBody caps a heading's body excerpt in the signal, so heading-heavy docs
// stay retrievable by more than just their titles.
const maxHeadBody = 120

// maxSignalSize caps the whole signal so one long doc can't dominate memory or matching.
const maxSignalSize = 8000

func buildSignal(path, content string) string {
	var b strings.Builder
	b.WriteString(baseFromPath(path))
	b.WriteString("\n")
	loc := headingRe.FindAllStringSubmatchIndex(content, -1)
	for i, l := range loc {
		b.WriteString(strings.TrimSpace(content[l[2]:l[3]]))
		b.WriteString("\n")
		start := l[1]
		end := len(content)
		if i+1 < len(loc) {
			end = loc[i+1][0]
		}
		if seg := firstSentence(content[start:end], maxHeadBody); seg != "" {
			b.WriteString(seg)
			b.WriteString("\n")
		}
	}
	if len(loc) < 2 {
		body := content
		if len(body) > 4000 {
			body = body[:4000]
		}
		b.WriteString(body)
	}
	s := b.String()
	if utf8.RuneCountInString(s) > maxSignalSize {
		var sb strings.Builder
		n := 0
		for _, r := range s {
			if n >= maxSignalSize {
				break
			}
			sb.WriteRune(r)
			n++
		}
		s = sb.String()
	}
	return strings.ToLower(s)
}

func firstSentence(s string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= max {
			break
		}
		b.WriteRune(r)
		n++
		if r == '。' || r == '！' || r == '？' || r == '\n' {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

// tokenize splits on non-alphanumeric runes; pure-Han runs are skipped (bigram-matched).
func tokenize(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := fields[:0]
	for _, f := range fields {
		// Skip pure-Han runs (matched via bigrams) but keep latin/alnum words.
		if isAllHan(f) {
			continue
		}
		out = append(out, f)
	}
	return out
}

func isAllHan(s string) bool {
	for _, r := range s {
		if !isHan(r) {
			return false
		}
	}
	return s != ""
}

func isHan(r rune) bool { return r >= 0x4E00 && r <= 0x9FFF }

func sharedHanBigrams(a, b string) int {
	ga := hanBigrams(a)
	gb := hanBigrams(b)
	n := 0
	for g := range ga {
		if gb[g] {
			n++
		}
	}
	return n
}

func hanBigrams(s string) map[string]bool {
	out := map[string]bool{}
	runes := []rune(s)
	for i := 0; i+1 < len(runes); i++ {
		if isHan(runes[i]) && isHan(runes[i+1]) {
			out[string(runes[i:i+2])] = true
		}
	}
	return out
}

// normalizeDocPath canonicalizes deps/skill paths: forward slashes, with a leading
// "context/" segment stripped (deps are written as "context/..." but docs are relative).
func normalizeDocPath(p string) string {
	p = filepath.ToSlash(strings.TrimSpace(p))
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "context/")
	return p
}

func parseTriggerArray(val string) []string {
	val = strings.Trim(val, "[]")
	parts := strings.Split(val, ",")
	var result []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			result = append(result, p)
		}
	}
	return result
}

// List returns all skill definitions without Content — clients fetch it via Get.
func (m *Matcher) List() []agent.SkillDef {
	m.refreshIfChanged()
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]agent.SkillDef, len(m.skills))
	for i, sk := range m.skills {
		out[i] = agent.SkillDef{
			Name: sk.Name, Description: sk.Description, Path: sk.path,
			Triggers: sk.Triggers, Deps: sk.Deps,
		}
	}
	return out
}

// Get returns a skill definition by name, lazily loading its content from disk.
func (m *Matcher) Get(name string) (agent.SkillDef, bool) {
	m.refreshIfChanged()
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, sk := range m.skills {
		if sk.Name == name {
			def := agent.SkillDef{
				Name: sk.Name, Description: sk.Description, Path: sk.path,
				Triggers: sk.Triggers, Deps: sk.Deps,
			}
			if c, err := m.ReadDoc(sk.path); err == nil && strings.TrimSpace(c) != "" {
				def.Content = strings.TrimSpace(c)
			}
			return def, true
		}
	}
	return agent.SkillDef{}, false
}

// Save writes a skill document to the runtime dir (embedded-only matchers are read-only).
func (m *Matcher) Save(def agent.SkillDef) error {
	if m.dir == "" {
		return fmt.Errorf("skills: matcher is read-only (no runtime dir)")
	}
	if def.Name == "" {
		return fmt.Errorf("skills: name required")
	}
	// Reject chars that would corrupt the "name:" frontmatter line and orphan the file.
	if strings.ContainsAny(def.Name, "/\\\n\r") || strings.Contains(def.Name, "..") {
		return fmt.Errorf("skills: invalid skill name %q", def.Name)
	}
	doc := renderSkill(def)
	dst := filepath.Join(m.dir, "skill", def.Name+".md")
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(dst, []byte(doc), 0644); err != nil {
		return err
	}
	// Incremental refresh: only that file re-scans.
	return m.RefreshDoc("skill/" + def.Name + ".md")
}

// Delete removes a skill document by name and reloads.
func (m *Matcher) Delete(name string) error {
	if m.dir == "" {
		return fmt.Errorf("skills: matcher is read-only (no runtime dir)")
	}
	var target string
	_ = filepath.WalkDir(m.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(m.dir, path)
		if relErr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		if !isSkillFile(rel) {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if parseSkill(string(data), rel).Name == name {
			target = path
		}
		return nil
	})
	if target == "" {
		return fmt.Errorf("skills: %q not found", name)
	}
	if err := os.Remove(target); err != nil {
		return err
	}
	// Incremental refresh: only that entry is removed (restored if embedded default exists).
	rel, relErr := filepath.Rel(m.dir, target)
	if relErr != nil {
		rel = filepath.Base(target)
	}
	return m.RefreshDoc(filepath.ToSlash(rel))
}

func renderSkill(def agent.SkillDef) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: " + def.Name + "\n")
	if def.Description != "" {
		b.WriteString("description: " + def.Description + "\n")
	}
	if len(def.Triggers) > 0 {
		b.WriteString("triggers: [" + strings.Join(def.Triggers, ", ") + "]\n")
	}
	if len(def.Deps) > 0 {
		b.WriteString("deps: [" + strings.Join(def.Deps, ", ") + "]\n")
	}
	b.WriteString("---\n\n")
	b.WriteString(def.Content)
	if !strings.HasSuffix(def.Content, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

type emptyFS struct{}

func (emptyFS) Open(name string) (fs.File, error) {
	if name == "." {
		return emptyDir{}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

type emptyDir struct{}

func (emptyDir) Stat() (fs.FileInfo, error)         { return emptyDirInfo{}, nil }
func (emptyDir) Read([]byte) (int, error)           { return 0, fs.ErrInvalid }
func (emptyDir) Close() error                       { return nil }
func (emptyDir) ReadDir(int) ([]fs.DirEntry, error) { return nil, nil }

type emptyDirInfo struct{}

func (emptyDirInfo) Name() string       { return "." }
func (emptyDirInfo) Size() int64        { return 0 }
func (emptyDirInfo) Mode() os.FileMode  { return os.ModeDir | 0555 }
func (emptyDirInfo) ModTime() time.Time { return time.Time{} }
func (emptyDirInfo) IsDir() bool        { return true }
func (emptyDirInfo) Sys() any           { return nil }

// AppendRule appends a rule line to the current month's file under userdata/rule/,
// creating it if missing — the feedback loop persists "correction → new rule".
func (m *Matcher) AppendRule(line string) error {
	if m.dir == "" {
		return fmt.Errorf("matcher: read-only (no runtime dir)")
	}
	month := time.Now().Format("2006-01")
	dst := filepath.Join(m.dir, "rule", month+".md")
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString("\n" + line + "\n"); err != nil {
		return err
	}
	// Incremental refresh: only that month's file re-scans.
	return m.RefreshDoc("rule/" + month + ".md")
}

func (m *Matcher) SaveRuleDir(content string) error {
	if m.dir == "" {
		return fmt.Errorf("matcher: read-only (no runtime dir)")
	}
	month := time.Now().Format("2006-01")
	dst := filepath.Join(m.dir, "rule", month+".md")
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(dst, []byte(content), 0644); err != nil {
		return err
	}
	// Incremental refresh: saving the month's rules re-scans only that file.
	return m.RefreshDoc("rule/" + month + ".md")
}
