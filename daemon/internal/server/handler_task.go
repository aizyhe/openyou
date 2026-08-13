package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

func parseDate(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", true
	}
	if _, err := time.Parse("2006-01-02", s); err == nil {
		return s, true
	}
	if len(s) >= 10 {
		d := s[:10]
		if _, err := time.Parse("2006-01-02", d); err == nil {
			return d, true
		}
	}
	return "", false
}

func handleListTasks(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Tasks == nil {
			writeErr(w, http.StatusServiceUnavailable, "tasks unavailable")
			return
		}
		tasks, err := deps.Tasks.List()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
	}
}

func handleCreateTask(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if deps.Tasks == nil {
			writeErr(w, http.StatusServiceUnavailable, "tasks unavailable")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var body struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			StartDate   string `json:"start_date"`
			EndDate     string `json:"end_date"`
			Priority    string `json:"priority"`
			Source      string `json:"source"`
			SessionID   string `json:"session_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		body.Title = strings.TrimSpace(body.Title)
		if body.Title == "" {
			writeErr(w, http.StatusBadRequest, "title required")
			return
		}
		// description carries full Star→Task output, so the cap must fit long five-section plans.
		if len([]rune(body.Title)) > 200 || len([]rune(body.Description)) > 20000 {
			writeErr(w, http.StatusBadRequest, "title max 200 chars, description max 20000")
			return
		}
		startDate, ok := parseDate(body.StartDate)
		if !ok {
			writeErr(w, http.StatusBadRequest, "invalid start_date; must be YYYY-MM-DD")
			return
		}
		endDate, ok := parseDate(body.EndDate)
		if !ok {
			writeErr(w, http.StatusBadRequest, "invalid end_date; must be YYYY-MM-DD")
			return
		}
		priority := normalizePriority(body.Priority)
		// Default to "user"; "conversation" is a Star-accepted task; reject unknown values.
		source := strings.TrimSpace(body.Source)
		switch source {
		case "":
			source = "user"
		case "user", "conversation":
		default:
			writeErr(w, http.StatusBadRequest, "invalid source; must be user|conversation")
			return
		}
		t, err := deps.Tasks.CreateFull(strings.TrimSpace(body.Title), strings.TrimSpace(body.Description),
			startDate, endDate, priority, source, strings.TrimSpace(body.SessionID), time.Now().UnixNano())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"task": t})
	}
}

// handleUpdateTask is PARTIAL update: only fields present in the body change.
// The UI toggles send {title,end_date,done} — full-replace would wipe
// description/start_date/priority on every toggle.
func handleUpdateTask(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if deps.Tasks == nil {
			writeErr(w, http.StatusServiceUnavailable, "tasks unavailable")
			return
		}
		id := r.PathValue("id")
		if id == "" {
			writeErr(w, http.StatusBadRequest, "missing task id")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var body struct {
			Title       *string `json:"title"`
			Description *string `json:"description"`
			StartDate   *string `json:"start_date"`
			EndDate     *string `json:"end_date"`
			Done        *bool   `json:"done"`
			Priority    *string `json:"priority"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		existing, err := deps.Tasks.Get(id)
		if err != nil {
			writeNotFoundError(w, err, "task")
			return
		}
		title := existing.Title
		if body.Title != nil {
			title = strings.TrimSpace(*body.Title)
			if title == "" {
				writeErr(w, http.StatusBadRequest, "title required")
				return
			}
		}
		description := existing.Description
		if body.Description != nil {
			description = strings.TrimSpace(*body.Description)
		}
		startDate := existing.StartDate
		if body.StartDate != nil {
			d, ok := parseDate(*body.StartDate)
			if !ok {
				writeErr(w, http.StatusBadRequest, "invalid start_date; must be YYYY-MM-DD")
				return
			}
			startDate = d
		}
		endDate := existing.EndDate
		if body.EndDate != nil {
			d, ok := parseDate(*body.EndDate)
			if !ok {
				writeErr(w, http.StatusBadRequest, "invalid end_date; must be YYYY-MM-DD")
				return
			}
			endDate = d
		}
		done := existing.Done
		if body.Done != nil {
			done = *body.Done
		}
		priority := existing.Priority
		if body.Priority != nil {
			priority = normalizePriority(*body.Priority)
		}
		if err := deps.Tasks.Update(id, title, description, startDate, endDate, done, priority); err != nil {
			writeNotFoundError(w, err, "task")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	}
}

func normalizePriority(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "high", "low":
		return strings.ToLower(strings.TrimSpace(p))
	default:
		return "normal"
	}
}

func handleDeleteTask(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Tasks == nil {
			writeErr(w, http.StatusServiceUnavailable, "tasks unavailable")
			return
		}
		id := r.PathValue("id")
		if id == "" {
			writeErr(w, http.StatusBadRequest, "missing task id")
			return
		}
		if err := deps.Tasks.Delete(id); err != nil {
			writeNotFoundError(w, err, "task")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	}
}
