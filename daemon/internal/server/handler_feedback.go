package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"openyou/daemon/internal/store/consult"
)

func handleSaveFeedback(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var body struct {
			SessionID  string `json:"session_id"`
			TurnIndex  int    `json:"turn_index"`
			Rating     string `json:"rating"`
			Correction string `json:"correction"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		switch body.Rating {
		case "useful", "not_useful", "inaccurate":
		default:
			writeErr(w, http.StatusBadRequest, "rating must be useful|not_useful|inaccurate")
			return
		}
		// No session → orphan row that would poison HasFeedback("") lookups.
		if strings.TrimSpace(body.SessionID) == "" {
			writeErr(w, http.StatusBadRequest, "session_id required")
			return
		}
		correction := strings.TrimSpace(body.Correction)
		if len([]rune(correction)) > 2000 {
			writeErr(w, http.StatusBadRequest, "correction too long (max 2000 chars)")
			return
		}

		if deps.Feedback != nil {
			if err := deps.Feedback.SaveFeedback(&consult.FeedbackEntry{
				SessionID:  body.SessionID,
				TurnIndex:  body.TurnIndex,
				Rating:     body.Rating,
				Correction: correction,
			}); err != nil {
				log.Printf("feedback: save: %v", err)
				writeErr(w, http.StatusInternalServerError, "feedback save failed")
				return
			}
		}

		switch body.Rating {
		case "inaccurate":
			if correction != "" {
				applyCorrectionAsRule(deps, body.SessionID, correction)
			}
		}

		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	}
}

// applyCorrectionAsRule appends to this month's rules file (userdata/rule/YYYY-MM.md)
// — rules are file-backed, user-editable markdown.
func applyCorrectionAsRule(deps Deps, sessionID, correction string) {
	// Strip newlines to prevent injection of extra rule lines into the markdown file.
	correction = strings.ReplaceAll(strings.TrimSpace(correction), "\n", " ")
	correction = strings.ReplaceAll(correction, "\r", " ")
	sessionID = strings.ReplaceAll(sessionID, "\n", "")
	sessionID = strings.ReplaceAll(sessionID, "\r", "")
	date := time.Now().Format("2006-01-02")
	line := fmt.Sprintf("- [%s] %s — from: session %s feedback", date, correction, sessionID)
	if deps.Skills != nil {
		_ = deps.Skills.AppendRule(line)
		// Invalidate the cached prefix so the new rule governs the next reply immediately.
		InvalidateStablePrefix()
	}
}

func handlePendingFeedback(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items := computePendingFeedback(deps, 3)
		if items == nil {
			items = []consult.FeedbackPendingItem{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"pending": items})
	}
}

// computePendingFeedback finds sessions stale ≥ staleDays with an assistant reply
// but no feedback — follow-up candidates. UpdatedAt is stored in unix nanoseconds.
func computePendingFeedback(deps Deps, staleDays int) []consult.FeedbackPendingItem {
	if deps.Sessions == nil || deps.Feedback == nil {
		return nil
	}
	sessions, err := deps.Sessions.List()
	if err != nil {
		return nil
	}
	// Cap at the 10 newest sessions to bound the N+1 lookups below.
	if len(sessions) > 10 {
		sessions = sessions[:10]
	}
	cutoff := time.Now().Add(-time.Duration(staleDays) * 24 * time.Hour).UnixNano()
	var out []consult.FeedbackPendingItem
	// N+1 is acceptable — the loop is capped to 10 sessions above.
	for _, s := range sessions {
		if s.UpdatedAt <= 0 {
			continue // skip zero-value timestamps
		}
		if s.UpdatedAt > cutoff {
			continue // too recent
		}
		has, err := deps.Feedback.HasFeedback(s.ID)
		if err != nil || has {
			continue
		}
		msgs, err := deps.Sessions.GetMessages(s.ID)
		if err != nil {
			continue
		}
		hasAssistant := false
		for _, m := range msgs {
			if m.Role == "assistant" {
				hasAssistant = true
				break
			}
		}
		if !hasAssistant {
			continue
		}
		days := int((time.Now().UnixNano() - s.UpdatedAt) / int64(24*time.Hour))
		if days < 0 {
			continue // future timestamp (e.g. from a corrupt/imported backup) — skip
		}
		out = append(out, consult.FeedbackPendingItem{SessionID: s.ID, Title: s.Title, Days: days})
	}
	return out
}
