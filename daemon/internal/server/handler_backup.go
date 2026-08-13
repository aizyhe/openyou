package server

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"openyou/daemon/internal/store/consult"
	"openyou/daemon/internal/store/memory"
	"openyou/daemon/internal/store/task"
)

// backupVersion is the archive format: 2.0 was the legacy single-JSON; 3.0 is
// the zip of userdata plus a manifest.json.
const backupVersion = "3.0"

// backupExportRequest carries frontend-only data (Star history, UI settings from
// WebView localStorage). api_key is deliberately absent — secrets never enter a backup.
type backupExportRequest struct {
	StarHistory []map[string]any `json:"star_history"`
	Settings    map[string]any   `json:"settings"`
}

// handleBackupExport writes a ZIP snapshot: every userdata file verbatim
// (symlinks and dot-* excluded) plus a manifest.json of DB-derived data and
// frontend star_history/settings. Missing sections record a warning.
func handleBackupExport(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var extra backupExportRequest
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
			_ = json.NewDecoder(r.Body).Decode(&extra)
		}

		manifest := map[string]any{
			"version":   backupVersion,
			"timestamp": time.Now().UnixNano(),
		}
		if extra.StarHistory != nil {
			manifest["star_history"] = extra.StarHistory
		}
		if extra.Settings != nil {
			manifest["settings"] = extra.Settings
		}
		var warnings []string

		if deps.Memory != nil {
			// Unbounded on purpose — dropping rows is not a backup.
			if mems, err := deps.Memory.ListAllForBackup(); err == nil {
				manifest["memories"] = mems
			} else {
				log.Printf("backup: export memories: %v", err)
				warnings = append(warnings, "memories export failed")
			}
		}
		if deps.Feedback != nil {
			if fbs, err := deps.Feedback.ListAllFeedback(); err == nil {
				manifest["feedback"] = fbs
			} else {
				log.Printf("backup: export feedback: %v", err)
				warnings = append(warnings, "feedback export failed")
			}
		}
		if deps.Sessions != nil {
			if sessions, err := deps.Sessions.ListWithMessages(); err == nil {
				manifest["sessions"] = sessions
			} else {
				log.Printf("backup: export sessions: %v", err)
				warnings = append(warnings, "sessions export failed")
			}
		}
		if deps.Tasks != nil {
			if tasks, err := deps.Tasks.List(); err == nil {
				manifest["tasks"] = tasks
			} else {
				log.Printf("backup: export tasks: %v", err)
				warnings = append(warnings, "tasks export failed")
			}
		}
		if len(warnings) > 0 {
			manifest["warnings"] = warnings
		}

		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)

		// Collect relative paths without buffering contents — reading whole large
		// files could OOM the daemon. Shippable = regular file, no symlinks or dot-*,
		// under a concept dir, and never the reserved manifest.json.
		var rels []string
		if deps.DocsDir != "" {
			_ = filepath.WalkDir(deps.DocsDir, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					// An unlistable dir would otherwise drop its subtree without a trace.
					warnings = append(warnings, "export walk failed: "+path)
					return nil
				}
				rel, rerr := filepath.Rel(deps.DocsDir, path)
				if rerr != nil || strings.HasPrefix(rel, "..") {
					return nil
				}
				if rel == "." {
					return nil // the root itself — keep walking
				}
				// Symlinks encode absolute host paths and must not ship.
				if d.Type()&fs.ModeSymlink != 0 {
					return nil
				}
				comps := strings.Split(filepath.ToSlash(rel), "/")
				for _, comp := range comps {
					if strings.HasPrefix(comp, ".") {
						if d.IsDir() {
							return filepath.SkipDir
						}
						return nil
					}
				}
				if d.IsDir() || !d.Type().IsRegular() {
					return nil
				}
				if rel == "manifest.json" {
					return nil // reserved for the manifest we write below
				}
				if !backupConceptDirs[comps[0]] {
					warnings = append(warnings, "skipped non-backup path: "+filepath.ToSlash(rel))
					return nil
				}
				rels = append(rels, filepath.ToSlash(rel))
				return nil
			})
		}
		sort.Strings(rels)

		// inside/rule/skill always ship as dir entries — import requires them to exist.
		for _, core := range []string{"inside", "rule", "skill"} {
			if _, err := zw.Create("userdata/" + core + "/"); err != nil {
				log.Printf("backup: core dir entry %s: %v", core, err)
			}
		}

		// Stream each file in sorted order, feeding the checksum as we go
		// (rel + \x00 + content per entry); import recomputes the same value to
		// detect a corrupted/tampered transfer.
		hh := sha256.New()
		for _, rel := range rels {
			src, ferr := os.Open(filepath.Join(deps.DocsDir, filepath.FromSlash(rel)))
			if ferr != nil {
				warnings = append(warnings, "file export failed: "+rel)
				continue
			}
			hh.Write([]byte(rel))
			hh.Write([]byte{0})
			dst, cerr := zw.Create("userdata/" + rel)
			if cerr != nil {
				src.Close()
				continue
			}
			// TeeReader feeds the same bytes to checksum and zip, so sha256 matches what shipped.
			if _, werr := io.Copy(dst, io.TeeReader(src, hh)); werr != nil {
				warnings = append(warnings, "file export failed: "+rel)
			}
			src.Close()
		}
		manifest["sha256"] = hex.EncodeToString(hh.Sum(nil))

		manData, _ := json.MarshalIndent(manifest, "", "  ")
		if mw, err := zw.Create("userdata/manifest.json"); err == nil {
			_, _ = mw.Write(manData)
		}
		if err := zw.Close(); err != nil {
			log.Printf("backup: close zip: %v", err)
			writeErr(w, http.StatusInternalServerError, "backup build failed")
			return
		}

		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="openyou-backup.zip"`)
		// Surface dropped sections via header instead of burying them in the manifest.
		if len(warnings) > 0 {
			w.Header().Set("X-Backup-Warnings", strconv.Itoa(len(warnings)))
			log.Printf("backup: export completed with %d warning(s)", len(warnings))
		}
		_, _ = w.Write(buf.Bytes())
	}
}

// handleBackupImport restores a v3 zip (raw bytes or base64 envelope), strictly
// validated and ADDITIVELY restored (add-only, never delete). Legacy v2
// single-JSON backups are no longer supported.
func handleBackupImport(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 256<<20) // 256MB
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "read failed")
			return
		}

		// Raw zip bytes (curl / E2E tests).
		if bytes.HasPrefix(body, []byte("PK\x03\x04")) {
			restoreZip(w, deps, body)
			return
		}

		var envelope map[string]any
		if err := json.Unmarshal(body, &envelope); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid backup")
			return
		}
		if fmt.Sprintf("%v", envelope["format"]) == "zip" {
			b64, _ := envelope["data"].(string)
			raw, derr := base64.StdEncoding.DecodeString(b64)
			if derr != nil {
				writeErr(w, http.StatusBadRequest, "invalid zip payload")
				return
			}
			restoreZip(w, deps, raw)
			return
		}
		writeErr(w, http.StatusBadRequest, "unsupported backup format: only the v3 zip (openyou-backup-*.zip) is accepted")
	}
}

// backupConceptDirs is the ONLY directory structure a v3 backup may carry — a
// foreign zip can never smuggle data into an openyou backup.
var backupConceptDirs = map[string]bool{
	"inside": true, "rule": true, "skill": true, "star": true,
	"task": true, "calendar": true, "memory": true, "consult": true,
}

// restoreZip restores a v3 archive ADDITIVELY: files written only if absent
// locally (local edits win), DB rows inserted only if missing (dedup by ID).
// The archive is strictly validated first and its sha256 re-verified if present.
func restoreZip(w http.ResponseWriter, deps Deps, data []byte) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid zip")
		return
	}

	type zipEntry struct {
		rel string
		f   *zip.File
	}
	var entries []zipEntry
	manifestFound := false
	seenDirs := map[string]bool{}
	for _, f := range zr.File {
		name := filepath.ToSlash(f.Name)
		isDirEntry := strings.HasSuffix(name, "/")
		check := strings.TrimSuffix(name, "/")
		if !strings.HasPrefix(check, "userdata/") && check != "userdata" {
			writeErr(w, http.StatusBadRequest, "not an openyou backup: unexpected entry "+name)
			return
		}
		if check == "userdata" {
			continue // the root dir entry itself
		}
		rel := strings.TrimPrefix(check, "userdata/")
		if rel == "manifest.json" {
			if isDirEntry {
				writeErr(w, http.StatusBadRequest, "not an openyou backup: manifest.json is not a directory")
				return
			}
			manifestFound = true
			continue
		}
		if !safeBackupRel(rel) {
			writeErr(w, http.StatusBadRequest, "not an openyou backup: invalid path "+name)
			return
		}
		top := rel
		if i := strings.IndexByte(rel, '/'); i >= 0 {
			top = rel[:i]
		}
		if !backupConceptDirs[top] {
			writeErr(w, http.StatusBadRequest, "not an openyou backup: unexpected path "+name)
			return
		}
		seenDirs[top] = true
		if isDirEntry {
			continue // validated; no content to restore
		}
		entries = append(entries, zipEntry{rel: rel, f: f})
	}
	if !manifestFound {
		writeErr(w, http.StatusBadRequest, "not an openyou backup: missing userdata/manifest.json")
		return
	}
	// inside/rule/skill are the identity core — the export always carries them.
	for _, req := range []string{"inside", "rule", "skill"} {
		if !seenDirs[req] {
			writeErr(w, http.StatusBadRequest, "not an openyou backup: missing userdata/"+req+"/")
			return
		}
	}

	var manifest map[string]any
	for _, f := range zr.File {
		if filepath.ToSlash(f.Name) != "userdata/manifest.json" {
			continue
		}
		rc, oerr := f.Open()
		if oerr != nil {
			writeErr(w, http.StatusBadRequest, "not an openyou backup: unreadable manifest.json")
			return
		}
		derr := json.NewDecoder(rc).Decode(&manifest)
		rc.Close()
		if derr != nil {
			writeErr(w, http.StatusBadRequest, "not an openyou backup: invalid manifest.json")
			return
		}
		break
	}

	// Recompute sha256 over the entries if present and reject a corrupted/tampered
	// transfer before restoring anything. Entries stream through the hash; the
	// request body is capped at 256MB upstream.
	if want, ok := manifest["sha256"].(string); ok && want != "" {
		sorted := make([]zipEntry, len(entries))
		copy(sorted, entries)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].rel < sorted[j].rel })
		hh := sha256.New()
		for _, e := range sorted {
			hh.Write([]byte(e.rel))
			hh.Write([]byte{0})
			rc, oerr := e.f.Open()
			if oerr != nil {
				writeErr(w, http.StatusBadRequest, "not an openyou backup: unreadable entry "+e.rel)
				return
			}
			_, rerr := io.Copy(hh, rc)
			rc.Close()
			if rerr != nil {
				writeErr(w, http.StatusBadRequest, "not an openyou backup: unreadable entry "+e.rel)
				return
			}
		}
		if got := hex.EncodeToString(hh.Sum(nil)); got != want {
			writeErr(w, http.StatusBadRequest, "backup corrupted: sha256 mismatch")
			return
		}
	}

	// Existing files are kept (user edits win); only missing ones are added. Files
	// stream to disk — an earlier version truncated >64MiB and still reported success.
	var failures []string
	counts := restoreCounts{}
	if deps.DocsDir != "" {
		for _, e := range entries {
			full := filepath.Join(deps.DocsDir, filepath.FromSlash(e.rel))
			if _, serr := os.Stat(full); serr == nil {
				continue // already present locally — keep the local version
			}
			if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
				failures = append(failures, "doc mkdir failed: "+e.rel)
				continue
			}
			rc, oerr := e.f.Open()
			if oerr != nil {
				failures = append(failures, "doc read failed: "+e.rel)
				continue
			}
			dst, cerr := os.Create(full)
			if cerr != nil {
				rc.Close()
				log.Printf("backup: doc create %s: %v", e.rel, cerr)
				failures = append(failures, "doc restore failed: "+e.rel)
				continue
			}
			_, werr := io.Copy(dst, rc)
			rc.Close()
			dst.Close()
			if werr != nil {
				log.Printf("backup: doc restore %s: %v", e.rel, werr)
				os.Remove(full) // never leave a half-written file behind
				failures = append(failures, "doc restore failed: "+e.rel)
				continue
			}
			counts.files++
		}
	}

	// DB rows are added additively (dedup by ID, never wipe); sections absent
	// from the manifest leave their table untouched.
	counts.merge(restoreManifestRows(deps, manifest, &failures))

	reloadAfterRestore(deps)
	respondImport(w, counts, failures, manifest)
}

// restoreCounts breaks down an import so the UI can show what was added.
type restoreCounts struct {
	files    int
	sessions int
	memories int
	tasks    int
	feedback int
}

func (c *restoreCounts) merge(o restoreCounts) {
	c.sessions += o.sessions
	c.memories += o.memories
	c.tasks += o.tasks
	c.feedback += o.feedback
}

func (c restoreCounts) total() int {
	return c.files + c.sessions + c.memories + c.tasks + c.feedback
}

// restoreManifestRows upserts rows by ID, leaving existing rows untouched;
// appends failures and returns per-category counts. File-backed docs are handled
// separately by the callers.
func restoreManifestRows(deps Deps, backup map[string]any, failures *[]string) restoreCounts {
	var counts restoreCounts

	if mems, ok := backup["memories"].([]interface{}); ok && deps.Memory != nil {
		for _, m := range mems {
			mm, ok := m.(map[string]interface{})
			if !ok {
				continue
			}
			entry := memoryFromMap(mm)
			if entry.Content == "" {
				continue
			}
			if _, err := deps.Memory.SaveImported(entry); err != nil {
				log.Printf("backup: memory restore: %v", err)
				*failures = append(*failures, "memory restore failed")
			} else {
				counts.memories++
			}
		}
	}

	if sessions, ok := backup["sessions"].([]interface{}); ok && deps.Sessions != nil && deps.DB != nil {
		for _, s := range sessions {
			sm, ok := s.(map[string]interface{})
			if !ok {
				continue
			}
			sid, _ := sm["id"].(string)
			if sid == "" {
				continue
			}
			title, _ := sm["title"].(string)
			createdAt, _ := sm["created_at"].(float64)
			updatedAt, _ := sm["updated_at"].(float64)

			// Each session gets its own transaction so one failure can't roll back the others.
			tx, txErr := deps.DB.Begin()
			if txErr != nil {
				log.Printf("backup: session %s tx begin: %v", sid, txErr)
				*failures = append(*failures, "session "+sid+" tx begin failed")
				continue
			}

			_, err := tx.Exec(
				`INSERT INTO consults (id, title, created_at, updated_at) VALUES (?, ?, ?, ?)
				 ON CONFLICT(id) DO NOTHING`,
				sid, title, int64(createdAt), int64(updatedAt),
			)
			if err != nil {
				tx.Rollback() //nolint:errcheck
				log.Printf("backup: session %s ensure: %v", sid, err)
				*failures = append(*failures, "session "+sid+" ensure failed")
				continue
			}
			importNow := time.Now().UnixNano()

			var msgCount int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM messages WHERE consult_id = ?`, sid).Scan(&msgCount); err != nil {
				log.Printf("backup: session %s count messages: %v", sid, err)
				*failures = append(*failures, "session "+sid+" count failed")
				tx.Rollback() //nolint:errcheck
				continue
			}
			if msgCount > 0 {
				tx.Rollback() //nolint:errcheck
				continue
			}
			msgs, _ := sm["messages"].([]interface{})
			sessionFailed := false
			for _, mv := range msgs {
				mmap, ok := mv.(map[string]interface{})
				if !ok {
					continue
				}
				role, _ := mmap["role"].(string)
				content, _ := mmap["content"].(string)
				if role == "" {
					continue
				}
				// Keep the original timestamp so the restored conversation keeps its spacing.
				ts := int64(importNow)
				if f, ok := mmap["created_at"].(float64); ok && f > 0 {
					ts = int64(f)
				}
				msgID := genBackupID("msg")
				if _, err := tx.Exec(
					`INSERT INTO messages (id, consult_id, role, content, created_at) VALUES (?, ?, ?, ?, ?)`,
					msgID, sid, role, content, ts,
				); err != nil {
					log.Printf("backup: session %s msg: %v", sid, err)
					*failures = append(*failures, "session "+sid+" msg failed")
					sessionFailed = true
					break
				}
			}
			if sessionFailed {
				tx.Rollback() //nolint:errcheck
				continue
			}
			if err := tx.Commit(); err != nil {
				log.Printf("backup: session %s commit: %v", sid, err)
				*failures = append(*failures, "session "+sid+" commit failed")
				continue
			}
			counts.sessions++
		}
	}

	if tasks, ok := backup["tasks"].([]interface{}); ok && deps.Tasks != nil {
		for _, t := range tasks {
			tm, ok := t.(map[string]interface{})
			if !ok {
				continue
			}
			id, _ := tm["id"].(string)
			title, _ := tm["title"].(string)
			description, _ := tm["description"].(string)
			startDate, _ := tm["start_date"].(string)
			endDate, _ := tm["end_date"].(string)
			source, _ := tm["source"].(string)
			sessionID, _ := tm["session_id"].(string)
			done, _ := tm["done"].(bool)
			priority, _ := tm["priority"].(string)
			createdAt, _ := tm["created_at"].(float64)
			updatedAt, _ := tm["updated_at"].(float64)
			if title == "" {
				continue
			}
			// Restore all serialized fields; Upsert defaults missing ones for older backups.
			if err := deps.Tasks.Upsert(&task.Task{
				ID:          id,
				Title:       title,
				Description: description,
				StartDate:   startDate,
				EndDate:     endDate,
				Source:      source,
				SessionID:   sessionID,
				Done:        done,
				Priority:    priority,
				CreatedAt:   int64(createdAt),
				UpdatedAt:   int64(updatedAt),
			}); err != nil {
				log.Printf("backup: task %s restore: %v", id, err)
				*failures = append(*failures, "task "+id+" restore failed")
			} else {
				counts.tasks++
			}
		}
	}

	// Feedback rows — ON CONFLICT DO NOTHING keeps locally-changed ratings (add-only).
	if fbs, ok := backup["feedback"].([]interface{}); ok && deps.Feedback != nil {
		for _, fv := range fbs {
			fm, ok := fv.(map[string]interface{})
			if !ok {
				continue
			}
			sid, _ := fm["session_id"].(string)
			rating, _ := fm["rating"].(string)
			if sid == "" || rating == "" {
				continue
			}
			id, _ := fm["id"].(string)
			turnF, _ := fm["turn_index"].(float64)
			corr, _ := fm["correction"].(string)
			createdF, _ := fm["created_at"].(float64)
			if err := deps.Feedback.RestoreFeedback(&consult.FeedbackEntry{
				ID:         id,
				SessionID:  sid,
				TurnIndex:  int(turnF),
				Rating:     rating,
				Correction: corr,
				CreatedAt:  int64(createdF),
			}); err != nil {
				log.Printf("backup: feedback restore: %v", err)
				*failures = append(*failures, "feedback restore failed")
			} else {
				counts.feedback++
			}
		}
	}

	return counts
}

// reloadAfterRestore refreshes the in-memory matcher and pseudonymizer caches
// so restored docs take effect without a daemon restart.
func reloadAfterRestore(deps Deps) {
	if deps.Skills != nil {
		_ = deps.Skills.Reload()
	}
	InvalidateStablePrefix()
	reseedPseudonymizer(deps)
}

// respondImport writes the restore result as a per-category breakdown; echoed
// star_history/settings let the frontend restore WebView-local state.
func respondImport(w http.ResponseWriter, counts restoreCounts, failures []string, manifest map[string]any) {
	resp := map[string]any{
		"restored":          counts.total(),
		"files_restored":    counts.files,
		"sessions_restored": counts.sessions,
		"memories_restored": counts.memories,
		"tasks_restored":    counts.tasks,
		"feedback_restored": counts.feedback,
	}
	if sh, ok := manifest["star_history"]; ok {
		resp["star_history"] = sh
	}
	if s, ok := manifest["settings"]; ok {
		resp["settings"] = s
	}
	if len(failures) > 0 {
		resp["ok"] = "false"
		resp["failures"] = failures
		writeJSON(w, http.StatusMultiStatus, resp)
		return
	}
	resp["ok"] = "true"
	writeJSON(w, http.StatusOK, resp)
}

// safeBackupRel rejects absolute paths and traversal/dot components so a crafted
// archive can't write outside userdata.
func safeBackupRel(rel string) bool {
	if rel == "" || filepath.IsAbs(rel) {
		return false
	}
	for _, comp := range strings.Split(filepath.ToSlash(rel), "/") {
		if comp == "" || comp == "." || comp == ".." || strings.HasPrefix(comp, ".") {
			return false
		}
	}
	return true
}

// memoryFromMap rebuilds a memory.MemoryEntry preserving all persisted fields so
// a restore doesn't reset timestamps, decay, or tags.
func memoryFromMap(m map[string]interface{}) memory.MemoryEntry {
	getS := func(k string) string { s, _ := m[k].(string); return s }
	getF := func(k string) float64 { f, _ := m[k].(float64); return f }
	getI := func(k string) int64 { f, _ := m[k].(float64); return int64(f) }
	getB := func(k string) bool { b, _ := m[k].(bool); return b }
	var tags []string
	if raw, ok := m["tags"].([]interface{}); ok {
		for _, t := range raw {
			if s, ok := t.(string); ok {
				tags = append(tags, s)
			}
		}
	}
	e := memory.MemoryEntry{
		ID:             getS("id"),
		Type:           memory.MemoryType(getS("type")),
		Person:         getS("person"),
		Content:        getS("content"),
		Confidence:     getF("confidence"),
		SourceSession:  getS("source_session"),
		Confirmed:      getB("confirmed"),
		Suggested:      getB("suggested"),
		Disputed:       getB("disputed"),
		DecayRate:      getF("decay_rate"),
		RelevanceScore: getF("relevance_score"),
		Tags:           tags,
		CreatedAt:      getI("created_at"),
		UpdatedAt:      getI("updated_at"),
	}
	return e
}

// genBackupID returns a random prefixed ID, falling back to a time-based suffix
// so IDs never collide on a zero buffer.
func genBackupID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%s_%x", prefix, time.Now().UnixNano())
	}
	return fmt.Sprintf("%s_%x", prefix, b)
}
