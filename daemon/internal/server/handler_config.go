package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// handleGetConfig: api_key is masked (last 4 chars visible) so the UI can show
// it's set without exposing the key.
func handleGetConfig(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Config == nil {
			writeErr(w, http.StatusInternalServerError, "config not configured")
			return
		}
		all, err := deps.Config.GetAll()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if all == nil {
			all = map[string]string{}
		}
		// Mask api_key — show only last 4 chars so UI knows whether it's set.
		raw, hasKey := all["api_key"]
		if hasKey && len(raw) > 4 {
			all["api_key"] = strings.Repeat("*", len(raw)-4) + raw[len(raw)-4:]
		} else {
			all["api_key"] = "****"
		}
		// has_key: an empty value means the file/fallback re-applies next boot.
		if hasKey && raw != "" {
			all["has_key"] = "true"
		} else {
			all["has_key"] = "false"
		}
		writeJSON(w, http.StatusOK, all)
	}
}

// handleUpdateConfig: api_key empty clears it (file fallback next boot), non-empty
// is stored encrypted and takes priority.
func handleUpdateConfig(deps Deps) http.HandlerFunc {
	allowed := map[string]bool{
		"api_key": true, "base_url": true, "model": true,
		"max_tokens": true, "temperature": true, "privacy_mode": true,
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Config == nil {
			writeErr(w, http.StatusInternalServerError, "config not configured")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		// Pass 1 — validate everything first so a bad value can't leave config half-updated.
		var updates []struct{ k, v string }
		for k, v := range body {
			if !allowed[k] {
				continue
			}
			s := toStr(v)
			// Reject values the LLM client would silently ignore — the user would think the change took effect.
			switch k {
			case "max_tokens":
				if n, err := strconv.Atoi(s); err != nil || n < 1 || n > 1000000 {
					writeErr(w, http.StatusBadRequest, "max_tokens must be an integer between 1 and 1000000")
					return
				}
			case "temperature":
				if f, err := strconv.ParseFloat(s, 64); err != nil || f < 0 || f > 2 {
					writeErr(w, http.StatusBadRequest, "temperature must be a number between 0 and 2")
					return
				}
			}
			updates = append(updates, struct{ k, v string }{k, s})
		}
		// Pass 2 — apply in deterministic order so behavior is predictable.
		sort.Slice(updates, func(i, j int) bool { return updates[i].k < updates[j].k })
		for _, u := range updates {
			if err := deps.Config.Set(u.k, u.v); err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			// Apply privacy mode live so the next LLM call reflects it.
			if u.k == "privacy_mode" && deps.Pseudonymizer != nil {
				deps.Pseudonymizer.SetEnabled(u.v != "trust")
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	}
}

func toStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		// Avoid scientific notation / trailing .0 for integers like max_tokens.
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	case nil:
		return ""
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}
