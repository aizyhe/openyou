package server

import (
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func handleListDocs(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.DocsDir == "" {
			log.Printf("docs: DocsDir empty — list all returning error")
			writeErr(w, http.StatusInternalServerError, "docs dir not configured")
			return
		}
		var files []map[string]any
		_ = filepath.WalkDir(deps.DocsDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
				return nil
			}
			rel, _ := filepath.Rel(deps.DocsDir, path)
			info, _ := d.Info()
			size := int64(0)
			if info != nil {
				size = info.Size()
			}
			previewStr := docPreview(path)
			files = append(files, map[string]any{
				"path":    filepath.ToSlash(rel),
				"size":    size,
				"preview": previewStr,
			})
			return nil
		})
		if files == nil {
			files = []map[string]any{}
		}
		log.Printf("docs: listing %s → %d files", deps.DocsDir, len(files))
		writeJSON(w, http.StatusOK, files)
	}
}

// docPreview builds a plain-text prefix (markdown stripped) that the UI clamps
// to two lines — the visible count is a display concern, not a data one.
func docPreview(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var parts []string
	total := 0
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimLeft(strings.TrimSpace(line), "#>-* \t")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts = append(parts, line)
		total += len([]rune(line))
		if total >= 240 {
			break
		}
	}
	preview := strings.Join(parts, " ")
	if runes := []rune(preview); len(runes) > 300 {
		preview = string(runes[:300]) + "…"
	}
	return preview
}

// reseedPseudonymizer refreshes the name list from inside docs after writes so
// runtime-added people are masked immediately, not only after a daemon restart.
func reseedPseudonymizer(deps Deps) {
	if deps.Pseudonymizer == nil || deps.Skills == nil {
		return
	}
	c, err := deps.Skills.ReadDir("inside")
	if err != nil {
		return
	}
	var names []string
	for _, line := range strings.Split(c, "\n") {
		if strings.HasPrefix(line, "# ") {
			names = append(names, strings.TrimSpace(strings.TrimPrefix(line, "# ")))
		}
	}
	deps.Pseudonymizer.UpdateNames(names)
}

func handleGetDoc(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.DocsDir == "" {
			writeErr(w, http.StatusInternalServerError, "docs dir not configured")
			return
		}
		rel := r.PathValue("path")
		if rel == "" || strings.Contains(rel, "..") {
			writeErr(w, http.StatusBadRequest, "invalid path")
			return
		}
		// Same contract as PUT: .md only, so the endpoint can't serve runtime DB/key files.
		if !strings.HasSuffix(strings.ToLower(rel), ".md") {
			writeErr(w, http.StatusBadRequest, "only .md files supported")
			return
		}
		full := filepath.Join(deps.DocsDir, filepath.FromSlash(rel))
		data, err := os.ReadFile(full)
		if err != nil {
			writeErr(w, http.StatusNotFound, "doc not found")
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Write(data)
	}
}

func handlePutDoc(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.DocsDir == "" {
			writeErr(w, http.StatusInternalServerError, "docs dir not configured")
			return
		}
		rel := r.PathValue("path")
		if rel == "" || strings.Contains(rel, "..") {
			writeErr(w, http.StatusBadRequest, "invalid path")
			return
		}
		if !strings.HasSuffix(strings.ToLower(rel), ".md") {
			writeErr(w, http.StatusBadRequest, "only .md files supported")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		// Decode before MkdirAll so an invalid payload can't leave empty dirs behind.
		var body struct{ Content string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		full := filepath.Join(deps.DocsDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := os.WriteFile(full, []byte(body.Content), 0644); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Incremental refresh: rescan only this file, so frequent saves stay snappy.
		if deps.Skills != nil {
			_ = deps.Skills.RefreshDoc(rel)
		}
		InvalidateStablePrefix()
		// A new/edited person must be masked on the next LLM call.
		reseedPseudonymizer(deps)
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	}
}

func handleDeleteDoc(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.DocsDir == "" {
			writeErr(w, http.StatusInternalServerError, "docs dir not configured")
			return
		}
		rel := r.PathValue("path")
		if rel == "" || strings.Contains(rel, "..") {
			writeErr(w, http.StatusBadRequest, "invalid path")
			return
		}
		// Symmetric with PUT: .md only, so arbitrary files can't be deleted.
		if !strings.HasSuffix(strings.ToLower(rel), ".md") {
			writeErr(w, http.StatusBadRequest, "only .md files supported")
			return
		}
		full := filepath.Join(deps.DocsDir, filepath.FromSlash(rel))
		if info, err := os.Stat(full); err != nil || info.IsDir() {
			writeErr(w, http.StatusNotFound, "doc not found")
			return
		}
		if err := os.Remove(full); err != nil {
			writeErr(w, http.StatusNotFound, "doc not found")
			return
		}
		// Incremental refresh: deleting only removes the in-memory entry — no full rescan.
		if deps.Skills != nil {
			_ = deps.Skills.RefreshDoc(rel)
		}
		// Deleted rules/insides must vanish from the prompt and pseudonymization immediately.
		InvalidateStablePrefix()
		reseedPseudonymizer(deps)
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	}
}
