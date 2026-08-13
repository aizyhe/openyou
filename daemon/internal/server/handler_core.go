package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr: for 5xx the internal message is logged but hidden from the client.
func writeErr(w http.ResponseWriter, code int, msg string) {
	if code >= 500 {
		log.Printf("server error %d: %s", code, msg)
		msg = "internal server error"
	}
	writeJSON(w, code, map[string]string{"error": msg})
}

// isNotFound relies on the shared %q "… not found" message contract across task/memory/consult.
func isNotFound(err error) bool {
	return strings.Contains(err.Error(), "not found")
}

func writeNotFoundError(w http.ResponseWriter, err error, resource string) {
	if isNotFound(err) {
		writeErr(w, http.StatusNotFound, resource+" not found")
		return
	}
	writeErr(w, http.StatusInternalServerError, err.Error())
}

func handleHealth(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
