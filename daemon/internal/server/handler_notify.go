package server

import (
	"net/http"
	"time"

	"openyou/daemon/internal/infra/notify"
)

// handleNotificationsPoll: the Android foreground service posts each payload as
// an OS notification; an empty result after the timeout means "poll again".
func handleNotificationsPoll(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Notify == nil {
			writeJSON(w, http.StatusOK, map[string]any{"notifications": []any{}})
			return
		}
		// Long-poll up to 25s (under typical 30s client/proxy read timeouts).
		items := deps.Notify.Poll(r.Context(), 25*time.Second)
		if items == nil {
			// Empty array (not null) so clients can always iterate.
			items = []notify.Payload{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"notifications": items})
	}
}
