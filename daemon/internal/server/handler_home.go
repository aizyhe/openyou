package server

import (
	"net/http"
	"regexp"
	"strings"

	"openyou/daemon/internal/store/consult"
)

var (
	reBriefingRule = regexp.MustCompile(`(?:^|\n)#[A-Za-z]?\d+\.`)
)

// handleHome returns the Home dashboard payload; field names match loadBriefing().
func handleHome(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := map[string]any{}

		peopleCount := 0
		if deps.Skills != nil {
			if c, err := deps.Skills.ReadDir("inside"); err == nil {
				for _, line := range strings.Split(c, "\n") {
					if strings.HasPrefix(line, "# ") {
						peopleCount++
					}
				}
			}
		}
		out["people_count"] = peopleCount

		rulesCount := 0
		if deps.Skills != nil {
			if rules, err := deps.Skills.ReadDir("rule"); err == nil {
				rulesCount = len(reBriefingRule.FindAllString(rules, -1))
			}
		}
		out["rules_count"] = rulesCount

		skillsCount := 0
		if deps.Skills != nil {
			skillsCount = len(deps.Skills.List())
		}
		out["skills_count"] = skillsCount

		memCount := 0
		if deps.Memory != nil {
			if n, err := deps.Memory.Count(); err == nil {
				memCount = n
			}
		}
		out["memory_count"] = memCount

		// Undone task count — task.Store has no ListPending, so filter here.
		taskCount := 0
		if deps.Tasks != nil {
			if tasks, err := deps.Tasks.List(); err == nil {
				for _, t := range tasks {
					if !t.Done {
						taskCount++
					}
				}
			}
		}
		out["task_count"] = taskCount

		// Session (Consult) count + recent sessions: one List() call, reused.
		sessionCount := 0
		recent := []map[string]string{}
		if deps.Sessions != nil {
			if sessions, err := deps.Sessions.List(); err == nil {
				sessionCount = len(sessions)
				for i, s := range sessions {
					if i >= 5 {
						break
					}
					recent = append(recent, map[string]string{"id": s.ID, "title": s.Title})
				}
			}
		}
		out["session_count"] = sessionCount
		out["recent_sessions"] = recent

		pending := computePendingFeedback(deps, 3)
		if pending == nil {
			out["pending_feedback"] = []any{}
		} else {
			out["pending_feedback"] = pending
		}

		memPending := 0
		if deps.Memory != nil {
			if unconf, err := deps.Memory.ListUnconfirmed(); err == nil {
				memPending = len(unconf)
			}
		}
		out["memory_pending"] = memPending

		// Daily question — the one proactive opener on Home: rule-based (zero LLM
		// cost), grounded in real pending state. Priority: feedback > insight > memory.
		out["daily_question"] = buildDailyQuestion(deps, pending)

		writeJSON(w, http.StatusOK, out)
	}
}

// buildDailyQuestion never calls the LLM — it selects from real pending state so
// Home has a "living" opener instead of a static list. Returns {} when nothing applies.
func buildDailyQuestion(deps Deps, pending []consult.FeedbackPendingItem) map[string]string {
	// Oldest un-followed-up advice — ask how it turned out.
	if len(pending) > 0 {
		oldest := pending[0]
		for _, p := range pending {
			if p.Days > oldest.Days {
				oldest = p
			}
		}
		return map[string]string{
			"kind":       "feedback",
			"text":       "上次关于「" + oldest.Title + "」的建议，" + itoaSafe(oldest.Days) + " 天了——有结果了吗？告诉我，我好调整。",
			"session_id": oldest.SessionID,
		}
	}
	// Unconfirmed memories → ask to confirm what he learned.
	if deps.Memory != nil {
		if unconf, err := deps.Memory.ListUnconfirmed(); err == nil && len(unconf) > 0 {
			return map[string]string{
				"kind": "memory",
				"text": "我从最近的对话里记下了 " + itoaSafe(len(unconf)) + " 条新东西，帮我确认下准不准？",
			}
		}
	}
	// Nothing actionable to surface — front-end skips the card entirely.
	return map[string]string{}
}
