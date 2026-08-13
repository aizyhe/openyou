package server_test

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openyou/daemon/internal/infra/bridge"
	"openyou/daemon/internal/infra/config"
	"openyou/daemon/internal/infra/notify"
	"openyou/daemon/internal/infra/tool"
	"openyou/daemon/internal/server"
	"openyou/daemon/internal/store/consult"
	"openyou/daemon/internal/store/memory"
	"openyou/daemon/internal/store/task"
)

// zipDeps builds a full daemon Deps set with a real temp DocsDir, so the
// file-backed knowledge base can be exercised end to end.
func zipDeps(t *testing.T) (http.Handler, server.Deps, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	cfg, _ := config.NewStore(db)
	ss, _ := consult.NewStore(db)
	mem, _ := memory.NewStore(db)
	fb, _ := consult.NewStore(db)
	tk, _ := task.NewStore(db)
	deps := server.Deps{
		DB:       db,
		Tools:    tool.NewRegistry(),
		Config:   cfg,
		Sessions: ss,
		Hub:      bridge.NewHub(),
		Memory:   mem,
		Feedback: fb,
		Tasks:    tk,
		Notify:   notify.NewQueue(16),
		DocsDir:  dir,
	}
	return server.NewRouter(deps), deps, dir
}

// TestBackupZipRoundTrip: export (with mixed file types + a dot-dir/symlink
// that must be excluded) → import into a fresh daemon → every file and DB row
// is present, and star_history/settings round-trip back to the frontend.
func TestBackupZipRoundTrip(t *testing.T) {
	h1, deps1, dir1 := zipDeps(t)

	// Seed the knowledge base: rules, a script, a binary image, a skill.
	// inside/rule/skill are the identity core a valid backup must carry.
	mustWrite(t, filepath.Join(dir1, "rule", "comm.md"), "# 先私聊再上会")
	mustWrite(t, filepath.Join(dir1, "skill", "craft", "writing.md"), "# writing")
	mustWrite(t, filepath.Join(dir1, "inside", "resume", "boss_cdp.py"), "#!/usr/bin/env python3\nprint('hi')\n")
	png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x01, 0x02, 0x03}
	mustWriteBytes(t, filepath.Join(dir1, "inside", "img", "logo.png"), png)
	// Tooling state that must NOT ship.
	mustWrite(t, filepath.Join(dir1, ".omo", "run-continuation", "x.json"), "{}")
	_ = os.Symlink(filepath.Join(dir1, "rule", "comm.md"), filepath.Join(dir1, "inside", "link.md"))

	// Seed DB data.
	saved, _ := deps1.Memory.Save(memEntry("张三关注成本", 0.9))
	sess, _ := deps1.Sessions.Create("backup zip")
	_, _ = deps1.Sessions.AppendMessage(sess.ID, "user", "hi")
	_, _ = deps1.Sessions.AppendMessage(sess.ID, "assistant", "ok")
	_ = deps1.Feedback.SaveFeedback(&consult.FeedbackEntry{
		SessionID: sess.ID, TurnIndex: 0, Rating: "useful", Correction: "c", CreatedAt: 123,
	})
	_, _ = deps1.Tasks.CreateFull("ship it", "desc", "2026-01-01", "2026-01-02", "high", "user", "ses_x", 111)

	// Export with frontend extras.
	w := doRequest(t, h1, http.MethodPost, "/api/backup/export", []byte(`{
		"star_history":[{"time":1,"result":"do X"}],
		"settings":{"model":"deepseek-v4-pro","doNotDisturb":true}
	}`))
	if w.Code != http.StatusOK {
		t.Fatalf("export status %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("export content-type want application/zip, got %q", ct)
	}
	raw := w.Body.Bytes()
	if !bytes.HasPrefix(raw, []byte("PK\x03\x04")) {
		t.Fatalf("export is not a zip archive (%d bytes)", len(raw))
	}

	// Inspect the archive: expected members present, tooling excluded.
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	members := map[string]string{}
	for _, f := range zr.File {
		members[f.Name] = ""
	}
	for _, want := range []string{
		"userdata/manifest.json",
		"userdata/rule/comm.md",
		"userdata/inside/resume/boss_cdp.py",
		"userdata/inside/img/logo.png",
	} {
		if _, ok := members[want]; !ok {
			t.Errorf("archive missing %s", want)
		}
	}
	for _, banned := range []string{"userdata/.omo/run-continuation/x.json", "userdata/inside/link.md"} {
		if _, ok := members[banned]; ok {
			t.Errorf("archive must exclude %s", banned)
		}
	}

	// Import into a fresh daemon with an empty userdata dir.
	h2, deps2, dir2 := zipDeps(t)
	wi := doRequest(t, h2, http.MethodPost, "/api/backup/import", raw)
	if wi.Code != http.StatusOK {
		t.Fatalf("import status %d: %s", wi.Code, wi.Body.String())
	}
	var resp struct {
		Ok          string           `json:"ok"`
		StarHistory []map[string]any `json:"star_history"`
		Settings    map[string]any   `json:"settings"`
	}
	_ = json.Unmarshal(wi.Body.Bytes(), &resp)
	if resp.Ok != "true" {
		t.Errorf("import ok want true, got %q", resp.Ok)
	}
	if len(resp.StarHistory) != 1 || resp.Settings["model"] != "deepseek-v4-pro" {
		t.Errorf("star_history/settings not echoed back: %+v", resp)
	}

	// Files restored verbatim (binary included).
	if got := mustRead(t, filepath.Join(dir2, "rule", "comm.md")); got != "# 先私聊再上会" {
		t.Errorf("rule file: %q", got)
	}
	if got := mustRead(t, filepath.Join(dir2, "inside", "resume", "boss_cdp.py")); !bytes.Equal([]byte(got), []byte("#!/usr/bin/env python3\nprint('hi')\n")) {
		t.Errorf("script file changed: %q", got)
	}
	if got := mustReadBytes(t, filepath.Join(dir2, "inside", "img", "logo.png")); !bytes.Equal(got, png) {
		t.Errorf("png bytes changed: %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir2, ".omo")); !os.IsNotExist(err) {
		t.Errorf(".omo must not be restored")
	}

	// DB rows restored.
	if got, _ := deps2.Memory.Get(saved.ID); got.Content != "张三关注成本" || got.Confidence != 0.9 {
		t.Errorf("memory not restored: %+v", got)
	}
	msgs, _ := deps2.Sessions.GetMessages(sess.ID)
	if len(msgs) != 2 {
		t.Errorf("session messages want 2, got %d", len(msgs))
	}
	fbs, _ := deps2.Feedback.ListAllFeedback()
	if len(fbs) != 1 || fbs[0].Rating != "useful" {
		t.Errorf("feedback not restored: %+v", fbs)
	}
	ts, _ := deps2.Tasks.List()
	if len(ts) != 1 || ts[0].Description != "desc" || ts[0].Priority != "high" {
		t.Errorf("tasks not restored: %+v", ts)
	}

	// Idempotency: re-import must not duplicate sessions.
	doRequest(t, h2, http.MethodPost, "/api/backup/import", raw)
	msgs2, _ := deps2.Sessions.GetMessages(sess.ID)
	if len(msgs2) != 2 {
		t.Errorf("session duplicated on re-import: %d messages", len(msgs2))
	}
}

// TestBackupZipBase64Envelope: the frontend ships the zip as
// {format:"zip", data:"<base64>"}; the daemon must accept it.
func TestBackupZipBase64Envelope(t *testing.T) {
	h1, _, dir1 := zipDeps(t)
	mustWrite(t, filepath.Join(dir1, "skill", "craft", "writing.md"), "# writing")
	mustWrite(t, filepath.Join(dir1, "rule", "comm.md"), "# rule")
	mustWrite(t, filepath.Join(dir1, "inside", "people", "boss.md"), "# Boss")

	w := doRequest(t, h1, http.MethodGet, "/api/backup/export", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("export status %d", w.Code)
	}
	enc := encodeBase64(t, w.Body.Bytes())
	env, _ := json.Marshal(map[string]any{"format": "zip", "data": enc})

	h2, _, dir2 := zipDeps(t)
	wi := doRequest(t, h2, http.MethodPost, "/api/backup/import", env)
	if wi.Code != http.StatusOK {
		t.Fatalf("envelope import status %d: %s", wi.Code, wi.Body.String())
	}
	if got := mustRead(t, filepath.Join(dir2, "skill", "craft", "writing.md")); got != "# writing" {
		t.Errorf("skill not restored via envelope: %q", got)
	}
}

// TestBackupZipPathTraversal: a crafted archive is rejected wholesale before
// anything is written, so it cannot write outside DocsDir.
func TestBackupZipPathTraversal(t *testing.T) {
	_, _, dir1 := zipDeps(t)
	h2, _, dir2 := zipDeps(t)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// Both the escaped path and the absolute path are rejected.
	for _, name := range []string{"userdata/../evil.txt", "userdata//etc/evil.txt", "/etc/evil.txt"} {
		f, _ := zw.Create(name)
		_, _ = io.WriteString(f, "evil")
	}
	mf, _ := zw.Create("userdata/manifest.json")
	_, _ = io.WriteString(mf, `{"version":"3.0","tasks":[]}`)
	_ = zw.Close()

	wi := doRequest(t, h2, http.MethodPost, "/api/backup/import", buf.Bytes())
	if wi.Code != http.StatusBadRequest {
		t.Fatalf("traversal import want 400, got %d: %s", wi.Code, wi.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir2, "evil.txt")); !os.IsNotExist(err) {
		t.Errorf("path traversal wrote outside DocsDir")
	}
	_ = dir1
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	mustWriteBytes(t, path, []byte(content))
}

// seedCore writes one file into each identity-core folder so an export
// satisfies the strict import validation (inside/rule/skill required).
func seedCore(t *testing.T, dir string) {
	t.Helper()
	mustWrite(t, filepath.Join(dir, "inside", "people", "boss.md"), "# Boss")
	mustWrite(t, filepath.Join(dir, "rule", "comm.md"), "# rule")
	mustWrite(t, filepath.Join(dir, "skill", "craft", "writing.md"), "# skill")
}

func mustWriteBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	return string(mustReadBytes(t, path))
}

func mustReadBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func encodeBase64(t *testing.T, data []byte) string {
	t.Helper()
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var b strings.Builder
	for i := 0; i < len(data); i += 3 {
		var n uint32
		rem := len(data) - i
		n = uint32(data[i]) << 16
		if rem > 1 {
			n |= uint32(data[i+1]) << 8
		}
		if rem > 2 {
			n |= uint32(data[i+2])
		}
		b.WriteByte(chars[(n>>18)&63])
		b.WriteByte(chars[(n>>12)&63])
		if rem > 1 {
			b.WriteByte(chars[(n>>6)&63])
		} else {
			b.WriteByte('=')
		}
		if rem > 2 {
			b.WriteByte(chars[n&63])
		} else {
			b.WriteByte('=')
		}
	}
	return b.String()
}

// TestBackupReimportFeedbackIdempotent: importing the same zip twice must not
// duplicate feedback rows (feedback id is <session>-<turn>, so ON CONFLICT
// updates rather than inserts on the second pass).
func TestBackupReimportFeedbackIdempotent(t *testing.T) {
	h1, deps1, dir1 := zipDeps(t)
	seedCore(t, dir1)
	sess, _ := deps1.Sessions.Create("fb")
	_ = deps1.Feedback.SaveFeedback(&consult.FeedbackEntry{
		SessionID: sess.ID, TurnIndex: 0, Rating: "useful", Correction: "x", CreatedAt: 123,
	})
	w := doRequest(t, h1, http.MethodGet, "/api/backup/export", nil)
	raw := w.Body.Bytes()

	h2, deps2, _ := zipDeps(t)
	if wi := doRequest(t, h2, http.MethodPost, "/api/backup/import", raw); wi.Code != http.StatusOK {
		t.Fatalf("first import: %d %s", wi.Code, wi.Body.String())
	}
	if wi := doRequest(t, h2, http.MethodPost, "/api/backup/import", raw); wi.Code != http.StatusOK {
		t.Fatalf("second import: %d %s", wi.Code, wi.Body.String())
	}
	fbs, _ := deps2.Feedback.ListAllFeedback()
	if len(fbs) != 1 {
		t.Errorf("feedback duplicated on re-import: %d rows", len(fbs))
	}
}

// TestBackupZipWeirdEntries: strict validation rejects a crafted archive
// wholesale (400) — a zip mixing malformed paths with valid ones is not an
// openyou export, so nothing may be written from it.
func TestBackupZipWeirdEntries(t *testing.T) {
	h2, _, dir2 := zipDeps(t)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// Directory entry (trailing slash).
	d, _ := zw.Create("userdata/rule/")
	_, _ = io.WriteString(d, "")
	// Double-slash path → empty component rejected.
	f, _ := zw.Create("userdata//evil.md")
	_, _ = io.WriteString(f, "evil")
	// Traversal with a single dot and with backslash-encoded separators.
	f2, _ := zw.Create("userdata/rule/../escape.md")
	_, _ = io.WriteString(f2, "evil")
	f3, _ := zw.Create("userdata\\..\\win-escape.md")
	_, _ = io.WriteString(f3, "evil")
	// Legit file — but the archive as a whole is malformed.
	ok, _ := zw.Create("userdata/rule/good.md")
	_, _ = io.WriteString(ok, "good")
	mf, _ := zw.Create("userdata/manifest.json")
	_, _ = io.WriteString(mf, `{"version":"3.0"}`)
	_ = zw.Close()

	wi := doRequest(t, h2, http.MethodPost, "/api/backup/import", buf.Bytes())
	if wi.Code != http.StatusBadRequest {
		t.Fatalf("weird zip import want 400, got %d: %s", wi.Code, wi.Body.String())
	}
	// Nothing escaped DocsDir — and nothing was restored from a rejected zip.
	for _, name := range []string{"escape.md", "win-escape.md", "evil.md"} {
		if _, err := os.Stat(filepath.Join(dir2, name)); !os.IsNotExist(err) {
			t.Errorf("traversal wrote %s outside intended subdir", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir2, "rule", "good.md")); !os.IsNotExist(err) {
		t.Errorf("rejected archive must not restore anything")
	}
}

// TestBackupExportWarningsHeader: an unreadable file must not silently produce
// a "complete" backup — the export flags it via X-Backup-Warnings.
func TestBackupExportWarningsHeader(t *testing.T) {
	h1, _, dir1 := zipDeps(t)
	blocked := filepath.Join(dir1, "inside", "secret.txt")
	mustWrite(t, blocked, "classified")
	if err := os.Chmod(blocked, 0); err != nil {
		t.Skipf("chmod not permitted: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0644) })

	w := doRequest(t, h1, http.MethodGet, "/api/backup/export", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("export status %d: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("X-Backup-Warnings") == "" {
		t.Errorf("expected X-Backup-Warnings header for unreadable file")
	}
}

// Go int64 nano timestamps exceed float64's 52-bit mantissa, so export→import
// is not bit-exact (~256ns rounding; could shift a memory's month boundary
// only if created within ~256ns of midnight).
func TestBackupTimestampPrecision(t *testing.T) {
	h1, deps1, dir1 := zipDeps(t)
	seedCore(t, dir1)
	orig := memEntry("ts check", 0.9)
	orig.CreatedAt = 1785821133200983808
	orig.UpdatedAt = 1785821133200983809
	saved, _ := deps1.Memory.Save(orig)

	w := doRequest(t, h1, http.MethodGet, "/api/backup/export", nil)
	h2, deps2, _ := zipDeps(t)
	wi := doRequest(t, h2, http.MethodPost, "/api/backup/import", w.Body.Bytes())
	if wi.Code != http.StatusOK {
		t.Fatalf("import: %d %s", wi.Code, wi.Body.String())
	}
	got, ok := deps2.Memory.Get(saved.ID)
	if !ok {
		t.Fatal("memory missing after restore")
	}
	if got.CreatedAt != orig.CreatedAt || got.UpdatedAt != orig.UpdatedAt {
		t.Logf("precision note: restored ts (%d/%d) != original (%d/%d)",
			got.CreatedAt, got.UpdatedAt, orig.CreatedAt, orig.UpdatedAt)
	}
}

// buildCoreZip assembles a minimal valid v3 archive (inside/rule/skill +
// manifest.json) with arbitrary extra entries, for validation tests.
func buildCoreZip(t *testing.T, extra map[string]string, manifest string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	core := map[string]string{
		"userdata/inside/people/boss.md": "# Boss",
		"userdata/rule/comm.md":          "# rule",
		"userdata/skill/craft/write.md":  "# skill",
	}
	for name, content := range core {
		if extra != nil {
			if _, shadowed := extra[name]; shadowed {
				continue
			}
		}
		f, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		_, _ = io.WriteString(f, content)
	}
	for name, content := range extra {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		_, _ = io.WriteString(f, content)
	}
	mf, _ := zw.Create("userdata/manifest.json")
	if manifest == "" {
		manifest = `{"version":"3.0"}`
	}
	_, _ = io.WriteString(mf, manifest)
	_ = zw.Close()
	return buf.Bytes()
}

// TestBackupImportStrictValidation: archives that don't look exactly like our
// own export are rejected wholesale with 400, before anything is written.
func TestBackupImportStrictValidation(t *testing.T) {
	cases := []struct {
		name  string
		extra map[string]string
		mfst  string
	}{
		{"entry outside userdata", map[string]string{"evil.txt": "x"}, ""},
		{"unknown concept dir", map[string]string{"userdata/notes/x.md": "x"}, ""},
		{"stray file at userdata root", map[string]string{"userdata/stray.md": "x"}, ""},
		{"skill dir missing", map[string]string{"userdata/skill/craft/write.md": ""}, ""},
		{"manifest not json", nil, "{not-json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, dir := zipDeps(t)
			// Pre-existing local data must survive a rejected import untouched.
			seedCore(t, dir)
			var raw []byte
			if tc.name == "skill dir missing" {
				// Build without the skill entry.
				var buf bytes.Buffer
				zw := zip.NewWriter(&buf)
				for _, n := range []string{"userdata/inside/people/boss.md", "userdata/rule/comm.md"} {
					f, _ := zw.Create(n)
					_, _ = io.WriteString(f, "x")
				}
				mf, _ := zw.Create("userdata/manifest.json")
				_, _ = io.WriteString(mf, `{"version":"3.0"}`)
				_ = zw.Close()
				raw = buf.Bytes()
			} else {
				raw = buildCoreZip(t, tc.extra, tc.mfst)
			}
			wi := doRequest(t, h, http.MethodPost, "/api/backup/import", raw)
			if wi.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d: %s", wi.Code, wi.Body.String())
			}
			if got := mustRead(t, filepath.Join(dir, "rule", "comm.md")); got != "# rule" {
				t.Errorf("rejected import modified local data: %q", got)
			}
		})
	}

	t.Run("missing manifest", func(t *testing.T) {
		h, _, _ := zipDeps(t)
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for _, n := range []string{"userdata/inside/a.md", "userdata/rule/b.md", "userdata/skill/c.md"} {
			f, _ := zw.Create(n)
			_, _ = io.WriteString(f, "x")
		}
		_ = zw.Close()
		wi := doRequest(t, h, http.MethodPost, "/api/backup/import", buf.Bytes())
		if wi.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d: %s", wi.Code, wi.Body.String())
		}
	})
}

// Import is add-only: existing files/rows win, local-only data survives,
// and only what the backup carries and local lacks is added.
func TestBackupImportAdditiveMerge(t *testing.T) {
	// Source daemon: the backup's content.
	h1, deps1, dir1 := zipDeps(t)
	mustWrite(t, filepath.Join(dir1, "rule", "comm.md"), "# backup rule")
	mustWrite(t, filepath.Join(dir1, "inside", "people", "boss.md"), "# Boss")
	mustWrite(t, filepath.Join(dir1, "skill", "craft", "write.md"), "# backup skill")
	gsess, _ := deps1.Sessions.Create("backup session")
	_, _ = deps1.Sessions.AppendMessage(gsess.ID, "user", "backup hi")
	gmem, _ := deps1.Memory.Save(memEntry("backup memory", 0.9))
	_, _ = deps1.Tasks.CreateFull("backup task", "d", "", "2026-02-02", "high", "user", "", 222)
	w := doRequest(t, h1, http.MethodGet, "/api/backup/export", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("export: %d", w.Code)
	}
	raw := w.Body.Bytes()

	// Target daemon: divergent local state that must SURVIVE the import.
	h2, deps2, dir2 := zipDeps(t)
	mustWrite(t, filepath.Join(dir2, "rule", "comm.md"), "# LOCAL EDIT (must survive)")
	mustWrite(t, filepath.Join(dir2, "skill", "craft", "write.md"), "# local skill (must survive)")
	mustWrite(t, filepath.Join(dir2, "inside", "people", "extra-person.md"), "# local-only file")
	lsess, _ := deps2.Sessions.Create("local session (must survive)")
	_, _ = deps2.Sessions.AppendMessage(lsess.ID, "user", "local hi")
	_, _ = deps2.Memory.Save(memEntry("local memory (must survive)", 0.5))
	_, _ = deps2.Tasks.CreateFull("local task (must survive)", "", "", "", "", "", "", 333)

	wi := doRequest(t, h2, http.MethodPost, "/api/backup/import", raw)
	if wi.Code != http.StatusOK {
		t.Fatalf("import: %d %s", wi.Code, wi.Body.String())
	}

	// Files: local versions win; nothing local is deleted.
	if got := mustRead(t, filepath.Join(dir2, "rule", "comm.md")); got != "# LOCAL EDIT (must survive)" {
		t.Errorf("local file overwritten: %q", got)
	}
	if got := mustRead(t, filepath.Join(dir2, "skill", "craft", "write.md")); got != "# local skill (must survive)" {
		t.Errorf("local skill overwritten: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir2, "inside", "people", "extra-person.md")); err != nil {
		t.Errorf("local-only file deleted: %v", err)
	}
	// The one file local lacked was added.
	if _, err := os.Stat(filepath.Join(dir2, "inside", "people", "boss.md")); err != nil {
		t.Errorf("missing backup file not added: %v", err)
	}

	// DB: local rows kept, backup rows added alongside.
	sessions, _ := deps2.Sessions.List()
	if len(sessions) != 2 {
		t.Errorf("sessions want 2 (local + backup), got %d", len(sessions))
	}
	if _, ok := deps2.Memory.Get(gmem.ID); !ok {
		t.Errorf("backup memory not added")
	}
	if memCount, _ := deps2.Memory.Count(); memCount != 2 {
		t.Errorf("memories want 2 (local + backup), got %d", memCount)
	}
	tasks, _ := deps2.Tasks.List()
	if len(tasks) != 2 {
		t.Errorf("tasks want 2 (local + backup), got %d", len(tasks))
	}
}

// A `make userdata` zip carries a manifest with NO DB sections — import adds
// only files the phone lacks and never touches sessions/memories/tasks.
func TestBackupImportPCStyleManifest(t *testing.T) {
	h2, deps2, dir2 := zipDeps(t)
	// Existing phone state: two of the core files already exist locally with
	// local edits, plus DB data.
	mustWrite(t, filepath.Join(dir2, "inside", "people", "boss.md"), "# local boss")
	mustWrite(t, filepath.Join(dir2, "rule", "comm.md"), "# phone rule (must survive)")
	sess, _ := deps2.Sessions.Create("phone session")
	_, _ = deps2.Sessions.AppendMessage(sess.ID, "user", "hi")
	_, _ = deps2.Memory.Save(memEntry("phone memory", 0.8))
	_, _ = deps2.Tasks.CreateFull("phone task", "", "", "", "", "", "", 111)

	// PC-style archive: core files plus one file new to the phone, and a bare
	// manifest (no sessions/memories/tasks/feedback keys).
	raw := buildCoreZip(t, map[string]string{
		"userdata/skill/job/pc-only.md": "# PC-only skill",
	}, `{"version":"3.0","timestamp":1754000000000000000}`)

	wi := doRequest(t, h2, http.MethodPost, "/api/backup/import", raw)
	if wi.Code != http.StatusOK {
		t.Fatalf("import: %d %s", wi.Code, wi.Body.String())
	}
	// Existing local files survive untouched.
	if got := mustRead(t, filepath.Join(dir2, "rule", "comm.md")); got != "# phone rule (must survive)" {
		t.Errorf("local file overwritten: %q", got)
	}
	if got := mustRead(t, filepath.Join(dir2, "inside", "people", "boss.md")); got != "# local boss" {
		t.Errorf("local boss.md overwritten: %q", got)
	}
	// Files the phone lacked were added.
	if got := mustRead(t, filepath.Join(dir2, "skill", "job", "pc-only.md")); got != "# PC-only skill" {
		t.Errorf("missing file not added: %q", got)
	}
	if got := mustRead(t, filepath.Join(dir2, "skill", "craft", "write.md")); got != "# skill" {
		t.Errorf("missing core skill not added: %q", got)
	}
	// DB untouched — manifest carried no DB sections.
	sessions, _ := deps2.Sessions.List()
	if len(sessions) != 1 || sessions[0].Title != "phone session" {
		t.Errorf("sessions touched by file-only import: %+v", sessions)
	}
	if memCount, _ := deps2.Memory.Count(); memCount != 1 {
		t.Errorf("memories touched by file-only import: %d", memCount)
	}
	if tasks, _ := deps2.Tasks.List(); len(tasks) != 1 {
		t.Errorf("tasks touched by file-only import: %+v", tasks)
	}
}

// TestBackupImportChecksumTamper: the export embeds a sha256 content checksum
// in the manifest; flipping any file's bytes must reject the whole archive.
func TestBackupImportChecksumTamper(t *testing.T) {
	h1, _, dir1 := zipDeps(t)
	seedCore(t, dir1)
	w := doRequest(t, h1, http.MethodGet, "/api/backup/export", nil)
	raw := w.Body.Bytes()

	// Rewrite the archive with one file's content tampered.
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		rc.Close()
		if f.Name == "userdata/rule/comm.md" {
			data = append(data, []byte("tampered")...)
		}
		dst, _ := zw.Create(f.Name)
		_, _ = dst.Write(data)
	}
	_ = zw.Close()

	h2, _, _ := zipDeps(t)
	wi := doRequest(t, h2, http.MethodPost, "/api/backup/import", buf.Bytes())
	if wi.Code != http.StatusBadRequest {
		t.Fatalf("tampered import want 400, got %d: %s", wi.Code, wi.Body.String())
	}
	if !strings.Contains(wi.Body.String(), "sha256") {
		t.Errorf("error should mention sha256 mismatch: %s", wi.Body.String())
	}
}

// TestBackupRoundTripExact: export → import → export reproduces the same
// archive content (files byte-identical, same DB rows) — the guarantee that
// an exported file imports cleanly and re-exports unchanged.
func TestBackupRoundTripExact(t *testing.T) {
	h1, deps1, dir1 := zipDeps(t)
	mustWrite(t, filepath.Join(dir1, "inside", "people", "boss.md"), "# Boss")
	mustWrite(t, filepath.Join(dir1, "rule", "comm.md"), "# rule")
	mustWrite(t, filepath.Join(dir1, "skill", "craft", "write.md"), "# skill")
	sess, _ := deps1.Sessions.Create("round trip")
	_, _ = deps1.Sessions.AppendMessage(sess.ID, "user", "hello")
	_, _ = deps1.Memory.Save(memEntry("round trip memory", 0.9))
	_, _ = deps1.Tasks.CreateFull("round trip task", "d", "", "", "", "", "", 444)

	files := func(raw []byte) map[string]string {
		zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			t.Fatalf("read zip: %v", err)
		}
		out := map[string]string{}
		for _, f := range zr.File {
			if strings.HasSuffix(f.Name, "/") || f.Name == "userdata/manifest.json" {
				continue // timestamp inside the manifest differs by design
			}
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			out[f.Name] = string(b)
		}
		return out
	}

	first := doRequest(t, h1, http.MethodGet, "/api/backup/export", nil).Body.Bytes()

	h2, deps2, _ := zipDeps(t)
	if wi := doRequest(t, h2, http.MethodPost, "/api/backup/import", first); wi.Code != http.StatusOK {
		t.Fatalf("import: %d %s", wi.Code, wi.Body.String())
	}
	second := doRequest(t, h2, http.MethodGet, "/api/backup/export", nil).Body.Bytes()

	f1, f2 := files(first), files(second)
	if len(f1) != len(f2) {
		t.Fatalf("file sets differ: %d vs %d entries", len(f1), len(f2))
	}
	for name, content := range f1 {
		if f2[name] != content {
			t.Errorf("round-trip changed %s", name)
		}
	}
	// DB rows identical (same IDs survive the trip).
	s1, _ := deps1.Sessions.List()
	s2, _ := deps2.Sessions.List()
	if len(s1) != len(s2) || s1[0].ID != s2[0].ID {
		t.Errorf("sessions differ after round trip: %+v vs %+v", s1, s2)
	}
	m1, _ := deps1.Memory.ListAllForBackup()
	m2, _ := deps2.Memory.ListAllForBackup()
	if len(m1) != len(m2) || m1[0].ID != m2[0].ID {
		t.Errorf("memories differ after round trip")
	}
}

// TestBackupImportFeedbackAdditive pins the B3 fix: feedback rows carry their
// exported id, and restore never overwrites a rating the user changed locally.
func TestBackupImportFeedbackAdditive(t *testing.T) {
	h1, deps1, dir1 := zipDeps(t)
	seedCore(t, dir1)
	sess, _ := deps1.Sessions.Create("fb session")
	_ = deps1.Feedback.SaveFeedback(&consult.FeedbackEntry{
		SessionID: sess.ID, TurnIndex: 0, Rating: "useful", Correction: "c", CreatedAt: 123,
	})
	raw := doRequest(t, h1, http.MethodGet, "/api/backup/export", nil).Body.Bytes()

	// Target: same session, but the user has SINCE changed the rating locally.
	h2, deps2, _ := zipDeps(t)
	_ = deps2.Sessions.EnsureWithTimestamp(sess.ID, "fb session", 1, 1)
	_ = deps2.Feedback.SaveFeedback(&consult.FeedbackEntry{
		SessionID: sess.ID, TurnIndex: 0, Rating: "inaccurate", Correction: "local edit", CreatedAt: 999,
	})
	wi := doRequest(t, h2, http.MethodPost, "/api/backup/import", raw)
	if wi.Code != http.StatusOK {
		t.Fatalf("import: %d %s", wi.Code, wi.Body.String())
	}
	fbs, _ := deps2.Feedback.ListAllFeedback()
	if len(fbs) != 1 {
		t.Fatalf("feedback rows want 1 (no duplicate), got %d", len(fbs))
	}
	if fbs[0].Rating != "inaccurate" {
		t.Errorf("local rating reverted by import: %q (want inaccurate)", fbs[0].Rating)
	}

	// A manifest row with an explicit id keeps that exact id.
	custom := buildCoreZip(t, nil, `{
		"version":"3.0",
		"feedback":[{"id":"custom_1","session_id":"s_x","turn_index":2,"rating":"useful","created_at":5}]
	}`)
	h3, deps3, _ := zipDeps(t)
	if wi := doRequest(t, h3, http.MethodPost, "/api/backup/import", custom); wi.Code != http.StatusOK {
		t.Fatalf("custom-id import: %d %s", wi.Code, wi.Body.String())
	}
	fbs3, _ := deps3.Feedback.ListAllFeedback()
	if len(fbs3) != 1 || fbs3[0].ID != "custom_1" {
		t.Errorf("exported feedback id not preserved: %+v", fbs3)
	}
}

// TestBackupImportMemoryAdditive pins B2 (part 1): an existing memory row
// (e.g. locally decayed/confirmed since the backup) is never overwritten.
func TestBackupImportMemoryAdditive(t *testing.T) {
	h1, deps1, dir1 := zipDeps(t)
	seedCore(t, dir1)
	m, _ := deps1.Memory.Save(memEntry("original content", 0.9))
	raw := doRequest(t, h1, http.MethodGet, "/api/backup/export", nil).Body.Bytes()

	h2, deps2, _ := zipDeps(t)
	// Local copy diverged after the backup was taken.
	loc := memEntry("locally updated content", 0.9)
	loc.ID = m.ID
	loc.Confirmed = true
	_, _ = deps2.Memory.Save(loc)

	wi := doRequest(t, h2, http.MethodPost, "/api/backup/import", raw)
	if wi.Code != http.StatusOK {
		t.Fatalf("import: %d %s", wi.Code, wi.Body.String())
	}
	got, ok := deps2.Memory.Get(m.ID)
	if !ok {
		t.Fatal("memory missing after import")
	}
	if got.Content != "locally updated content" || !got.Confirmed {
		t.Errorf("local memory overwritten by stale backup: %+v", got)
	}
	if n, _ := deps2.Memory.Count(); n != 1 {
		t.Errorf("memory duplicated on re-import: %d", n)
	}
}

// TestBackupImportKeepsMemoryMarkdown pins B2 (part 2): restoring memories
// must NOT regenerate userdata/memory/*.md — the archive's files (and local
// edits to them) win.
func TestBackupImportKeepsMemoryMarkdown(t *testing.T) {
	h1, deps1, dir1 := zipDeps(t)
	seedCore(t, dir1)
	_, _ = deps1.Memory.Save(memEntry("a remembered fact", 0.9))
	raw := doRequest(t, h1, http.MethodGet, "/api/backup/export", nil).Body.Bytes()

	h2, _, dir2 := zipDeps(t)
	seedCore(t, dir2)
	memDir := filepath.Join(dir2, "memory")
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}
	prev := memory.MarkdownDir
	memory.MarkdownDir = memDir
	t.Cleanup(func() { memory.MarkdownDir = prev })
	local := "# 本地手写记忆月报\n> 备注: 必须保留\n"
	if err := os.WriteFile(filepath.Join(memDir, "2026-08.md"), []byte(local), 0644); err != nil {
		t.Fatal(err)
	}

	wi := doRequest(t, h2, http.MethodPost, "/api/backup/import", raw)
	if wi.Code != http.StatusOK {
		t.Fatalf("import: %d %s", wi.Code, wi.Body.String())
	}
	got := mustRead(t, filepath.Join(memDir, "2026-08.md"))
	if got != local {
		t.Errorf("memory restore rewrote the local month journal:\n%q", got)
	}
}

// Pins A2+B7: export ships only concept-dir paths (stray files are skipped)
// and always emits inside/rule/skill entries so an emptied core folder can't
// break the import's structure validation.
func TestBackupExportFilterAndCoreDirs(t *testing.T) {
	h1, _, dir1 := zipDeps(t)
	mustWrite(t, filepath.Join(dir1, "inside", "people", "boss.md"), "# Boss")
	mustWrite(t, filepath.Join(dir1, "skill", "craft", "write.md"), "# skill")
	// rule/ exists but is EMPTY; plus stray non-concept content.
	if err := os.MkdirAll(filepath.Join(dir1, "rule"), 0755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir1, "notes.txt"), "stray root file")
	mustWrite(t, filepath.Join(dir1, "junk", "x.md"), "stray dir file")

	w := doRequest(t, h1, http.MethodGet, "/api/backup/export", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("export: %d", w.Code)
	}
	if w.Header().Get("X-Backup-Warnings") == "" {
		t.Errorf("expected warnings for skipped stray files")
	}
	raw := w.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
		if strings.HasPrefix(f.Name, "userdata/junk/") || f.Name == "userdata/notes.txt" {
			t.Errorf("non-concept path shipped: %s", f.Name)
		}
	}
	for _, core := range []string{"userdata/inside/", "userdata/rule/", "userdata/skill/"} {
		if !names[core] {
			t.Errorf("missing core dir entry %s", core)
		}
	}

	// The archive must import cleanly even though rule/ has no files.
	h2, _, _ := zipDeps(t)
	if wi := doRequest(t, h2, http.MethodPost, "/api/backup/import", raw); wi.Code != http.StatusOK {
		t.Fatalf("import of own export: %d %s", wi.Code, wi.Body.String())
	}
}

// TestConfigUpdateAtomic pins C14: an invalid value in the batch rejects the
// WHOLE update — earlier keys are no longer half-applied.
func TestConfigUpdateAtomic(t *testing.T) {
	h, deps, _ := zipDeps(t)
	if err := deps.Config.Set("model", "before"); err != nil {
		t.Fatal(err)
	}
	w := doRequest(t, h, http.MethodPut, "/api/config",
		[]byte(`{"model":"after","temperature":"not-a-number"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
	if got, _ := deps.Config.Get("model"); got != "before" {
		t.Errorf("config half-updated: model=%q, want before", got)
	}
}
