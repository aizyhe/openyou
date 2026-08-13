package server

import (
	"net/http"
)

func handleListSessions(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Sessions == nil {
			writeJSON(w, http.StatusOK, map[string]any{"sessions": []any{}})
			return
		}
		sessions, err := deps.Sessions.List()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
	}
}

func handleGetSession(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Sessions == nil {
			writeErr(w, http.StatusServiceUnavailable, "sessions not configured")
			return
		}
		id := r.PathValue("id")
		sess, ok, err := deps.Sessions.Get(id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !ok {
			writeErr(w, http.StatusNotFound, "session not found")
			return
		}
		writeJSON(w, http.StatusOK, sess)
	}
}

func handleDeleteSession(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Sessions == nil {
			writeErr(w, http.StatusServiceUnavailable, "sessions not configured")
			return
		}
		id := r.PathValue("id")
		if err := deps.Sessions.Delete(id); err != nil {
			writeNotFoundError(w, err, "session")
			return
		}
		// Clear stale source_session references in memories.
		if deps.Memory != nil {
			_ = deps.Memory.ClearSessionRef(id)
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	}
}
