package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// CurationSuggestion is an ADVISORY skill-library cleanup; the user reviews and
// applies changes — the curator never modifies skills automatically.
type CurationSuggestion struct {
	Action string   `json:"action"`
	Skills []string `json:"skills"`
	Reason string   `json:"reason"`
}

// handleCurateSkills is a manual, LLM-driven dry-run review: it suggests
// merges/archives but NEVER changes anything — callers apply via existing skill CRUD.
func handleCurateSkills(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.LLM == nil {
			writeErr(w, http.StatusServiceUnavailable, "LLM not configured")
			return
		}
		// Curate also makes a full LLM call, so it must respect LLMSem.
		if deps.LLMSem != nil {
			select {
			case deps.LLMSem <- struct{}{}:
				defer func() { <-deps.LLMSem }()
			default:
				writeErr(w, http.StatusTooManyRequests, "too many concurrent requests")
				return
			}
		}
		if deps.SkillMatcher == nil {
			writeJSON(w, http.StatusOK, map[string]any{"suggestions": []any{}})
			return
		}
		skills := deps.SkillMatcher.List()
		if len(skills) < 2 {
			writeJSON(w, http.StatusOK, map[string]any{"suggestions": []any{}})
			return
		}

		var b strings.Builder
		b.WriteString("以下是当前技能库(每项:名称 | 描述 | 触发词)。找出应当\"合并\"的重复/高度重叠技能,")
		b.WriteString("以及应当\"归档\"的过期/无用技能。保守判断,宁可少提,不要误伤有独立价值的技能。\n\n")
		for _, s := range skills {
			b.WriteString("- " + s.Name + " | " + s.Description + " | " + strings.Join(s.Triggers, ",") + "\n")
		}

		sys := "你在审理用户的技能库,给出保守的整理建议(dry-run,不会自动执行)。" +
			"只输出 JSON 数组,每项含 action(\"merge\"|\"archive\")、skills(受影响技能名数组)、reason(一句话理由)。" +
			"没有值得整理的就返回空数组 []。不要臆造技能名,只用给定列表里的名称。"

		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		resp, err := deps.LLM.Chat(ctx, sys, b.String())
		if err != nil {
			// writeErr would mask self-fixable causes like "api_key not configured";
			// sanitizeLLMError already strips paths and truncates, so surface it.
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": sanitizeLLMError(err)})
			return
		}
		suggestions := parseCurations(resp, skillNameSet(deps))
		if suggestions == nil {
			suggestions = []CurationSuggestion{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"suggestions": suggestions})
	}
}

// skillNameSet filters hallucinated names the model might invent.
func skillNameSet(deps Deps) map[string]bool {
	set := map[string]bool{}
	if deps.SkillMatcher != nil {
		for _, s := range deps.SkillMatcher.List() {
			set[s.Name] = true
		}
	}
	return set
}

// parseCurations extracts the JSON array, dropping suggestions that reference unknown skill names.
func parseCurations(resp string, valid map[string]bool) []CurationSuggestion {
	resp = strings.TrimSpace(resp)
	if strings.HasPrefix(resp, "```") {
		resp = strings.TrimPrefix(resp, "```json")
		resp = strings.TrimPrefix(resp, "```")
		resp = strings.TrimSuffix(resp, "```")
		resp = strings.TrimSpace(resp)
	}
	if i := strings.IndexByte(resp, '['); i >= 0 {
		if j := strings.LastIndexByte(resp, ']'); j > i {
			resp = resp[i : j+1]
		}
	}
	var raw []CurationSuggestion
	if err := json.Unmarshal([]byte(resp), &raw); err != nil {
		return nil
	}
	var out []CurationSuggestion
	for _, s := range raw {
		kept := s.Skills[:0]
		for _, name := range s.Skills {
			if valid[name] {
				kept = append(kept, name)
			}
		}
		if len(kept) == 0 {
			continue // all names hallucinated — drop
		}
		s.Skills = kept
		out = append(out, s)
	}
	return out
}
