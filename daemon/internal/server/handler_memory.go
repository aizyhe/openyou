package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"openyou/daemon/internal/store/memory"
)

// validMemoryTypes is the accepted ?type= set; package-level so it's not re-allocated per request.
var validMemoryTypes = map[string]bool{
	"person_fact": true,
	"user_trait":  true,
	"rule":        true,
	"context":     true,
}

func handleListMemory(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		typeFilter := q.Get("type")
		personFilter := q.Get("person")
		confidenceFilter := q.Get("confidence")

		limit := 200
		offset := 0
		if v := q.Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				if n > 1000 {
					n = 1000
				}
				limit = n
			}
		}
		if v := q.Get("offset"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				offset = n
			}
		}

		if deps.Memory == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "memory not configured"})
			return
		}

		// Validate typeFilter against the allowed enum.
		if typeFilter != "" {
			if !validMemoryTypes[typeFilter] {
				writeErr(w, http.StatusBadRequest, "invalid type filter")
				return
			}
		}

		// Parse confidence threshold up front so we can reject bad values early.
		var minConf float64
		hasMinConf := false
		if confidenceFilter != "" {
			threshold, parseErr := strconv.ParseFloat(confidenceFilter, 64)
			if parseErr != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid confidence value"})
				return
			}
			minConf = threshold
			hasMinConf = true
		}

		opts := memory.FilterOpts{
			Type:       typeFilter,
			Person:     personFilter,
			MinConf:    minConf,
			HasMinConf: hasMinConf,
			Limit:      limit,
			Offset:     offset,
		}
		// Use COUNT(*) with the same filters — ListFiltered caps at 1000 rows, so len() would under-report.
		total, err := deps.Memory.CountFiltered(opts)
		if err != nil {
			log.Printf("memory: count filtered: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		entries, err := deps.Memory.ListFiltered(opts)
		if err != nil {
			log.Printf("memory: list filtered: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{"memories": entries, "total": total})
	}
}

func handleConfirmMemory(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Memory == nil {
			writeErr(w, http.StatusServiceUnavailable, "memory not configured")
			return
		}
		id := r.PathValue("id")
		if id == "" {
			writeErr(w, http.StatusBadRequest, "missing memory id")
			return
		}
		if err := deps.Memory.Confirm(id); err != nil {
			log.Printf("memory: confirm %q: %v", id, err)
			writeNotFoundError(w, err, "memory")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	}
}

func handleDisputeMemory(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Memory == nil {
			writeErr(w, http.StatusServiceUnavailable, "memory not configured")
			return
		}
		id := r.PathValue("id")
		if id == "" {
			writeErr(w, http.StatusBadRequest, "missing memory id")
			return
		}
		if err := deps.Memory.Dispute(id); err != nil {
			log.Printf("memory: dispute %q: %v", id, err)
			writeNotFoundError(w, err, "memory")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	}
}

func handleDeleteMemory(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Memory == nil {
			writeErr(w, http.StatusServiceUnavailable, "memory not configured")
			return
		}
		id := r.PathValue("id")
		if id == "" {
			writeErr(w, http.StatusBadRequest, "missing memory id")
			return
		}
		if err := deps.Memory.Delete(id); err != nil {
			log.Printf("memory: delete %q: %v", id, err)
			writeNotFoundError(w, err, "memory")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	}
}

func handleUpdateMemory(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		if deps.Memory == nil {
			writeErr(w, http.StatusServiceUnavailable, "memory not configured")
			return
		}
		id := r.PathValue("id")
		if id == "" {
			writeErr(w, http.StatusBadRequest, "missing memory id")
			return
		}
		var body struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		body.Content = strings.TrimSpace(body.Content)
		if body.Content == "" {
			writeErr(w, http.StatusBadRequest, "content required")
			return
		}
		entry, ok := deps.Memory.Get(id)
		if !ok {
			writeErr(w, http.StatusNotFound, "memory not found")
			return
		}
		entry.Content = body.Content
		// Bump UpdatedAt — Store.Save preserves non-zero values, and without this the
		// decay worker (clocked from UpdatedAt) would treat the edit as stale and
		// could delete the memory within one tick.
		entry.UpdatedAt = time.Now().UnixNano()
		if _, err := deps.Memory.Save(entry); err != nil {
			log.Printf("memory: update %q: %v", id, err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	}
}
