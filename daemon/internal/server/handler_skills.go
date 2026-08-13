package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"openyou/daemon/internal/ai/agent"
)

func handleListSkills(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.SkillMatcher == nil {
			writeJSON(w, http.StatusOK, map[string]any{"skills": []any{}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"skills": deps.SkillMatcher.List()})
	}
}

func handleGetSkill(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if deps.Skills == nil {
			writeErr(w, http.StatusServiceUnavailable, "skills are read-only")
			return
		}
		sk, ok := deps.Skills.Get(name)
		if !ok {
			writeErr(w, http.StatusNotFound, "skill not found")
			return
		}
		writeJSON(w, http.StatusOK, sk)
	}
}

func handleCreateSkill(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		saveSkillFromBody(deps, w, r, "")
	}
}

func handleUpdateSkill(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		saveSkillFromBody(deps, w, r, r.PathValue("name"))
	}
}

func saveSkillFromBody(deps Deps, w http.ResponseWriter, r *http.Request, pathName string) {
	if deps.Skills == nil {
		writeErr(w, http.StatusServiceUnavailable, "skills are read-only")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Triggers    any    `json:"triggers"`
		Deps        any    `json:"deps"`
		Content     string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	name := body.Name
	if name == "" {
		name = pathName
	}
	if name == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	// Preserve frontmatter deps when the client omits them: the UI editor has no
	// deps field, so treating absence as empty silently wiped declared deps on save.
	depsList := parseTriggers(body.Deps)
	if body.Deps == nil {
		if existing, ok := deps.Skills.Get(name); ok {
			depsList = existing.Deps
		}
	}
	def := agent.SkillDef{
		Name:        name,
		Description: body.Description,
		Triggers:    parseTriggers(body.Triggers),
		Deps:        depsList,
		Content:     body.Content,
	}
	if err := deps.Skills.Save(def); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// If the name changed (rename), delete the old skill file.
	if pathName != "" && name != pathName {
		if err := deps.Skills.Delete(pathName); err != nil {
			// Don't fail the save, but log loudly — a leftover old file would double-match queries.
			log.Printf("skills: rename %q -> %q: deleting old file failed: %v", pathName, name, err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func handleDeleteSkill(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Skills == nil {
			writeErr(w, http.StatusServiceUnavailable, "skills are read-only")
			return
		}
		name := r.PathValue("name")
		if err := deps.Skills.Delete(name); err != nil {
			writeNotFoundError(w, err, "skill")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	}
}

func handleReloadSkills(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Skills == nil {
			writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
			return
		}
		if err := deps.Skills.Reload(); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	}
}

func parseTriggers(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	case string:
		var out []string
		for _, p := range strings.Split(t, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	default:
		return nil
	}
}
