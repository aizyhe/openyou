package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// handleStar: one-tap advisor that streams a "what to do now" plan without
// persisting a session, then emits a suggest-task event for one-tap accept.
func handleStar(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if deps.LLM == nil {
			writeErr(w, http.StatusServiceUnavailable, "LLM not configured")
			return
		}
		if deps.LLMSem != nil {
			select {
			case deps.LLMSem <- struct{}{}:
				defer func() { <-deps.LLMSem }()
			default:
				writeErr(w, http.StatusTooManyRequests, "too many concurrent requests")
				return
			}
		}

		// Client may pass its local time; fall back to server time.
		var body struct {
			Now string `json:"now"` // e.g. "2026-07-28 15:04 Mon"
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		_ = json.NewDecoder(r.Body).Decode(&body)
		now := strings.TrimSpace(body.Now)
		if r := []rune(now); len(r) > 100 {
			now = string(r[:100])
		}
		if now == "" {
			now = time.Now().Format("2006-01-02 15:04")
		}

		prompt := buildDoPrompt(deps, now)

		sse, ok := newSSE(w)
		if !ok {
			writeErr(w, http.StatusInternalServerError, "streaming unsupported")
			return
		}

		ctx := r.Context()
		msgs := buildHistory(ctx, deps, "", prompt, "")
		scrub := &streamScrubber{}
		var fullText strings.Builder
		_, err := deps.LLM.Stream(ctx, msgs, func(delta string) {
			if safe := scrub.push(delta); safe != "" {
				fullText.WriteString(safe)
				sse.send(map[string]string{"type": "text-delta", "text": safe})
			}
		})
		if tail := scrub.flush(); tail != "" {
			fullText.WriteString(tail)
			sse.send(map[string]string{"type": "text-delta", "text": tail})
		}
		if err != nil {
			sse.send(map[string]string{"type": "error", "error": sanitizeLLMError(err)})
			sse.sendRaw("[DONE]")
			return
		}

		// Best-effort suggestion emitted BEFORE done/[DONE]: clients disconnect at the
		// terminator, which would cancel this call; the advice is already streamed, so
		// only the end-of-stream marker is delayed.
		if suggestion, err := suggestTaskFromDo(ctx, deps, fullText.String(), now); err == nil && suggestion != nil {
			payload, _ := json.Marshal(suggestion)
			sse.sendEvent("suggest-task", payload)
		}
		sse.send(map[string]string{"type": "done"})
		sse.sendRaw("[DONE]")
	}
}

type SuggestedTask struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	EndDate     string `json:"end_date"`
	Priority    string `json:"priority"`
}

// suggestTaskFromDo condenses the recommendation into one structured task. Returns
// nil (no error) when the LLM is unavailable or the output won't parse — the caller
// treats that as "no suggestion".
func suggestTaskFromDo(ctx context.Context, deps Deps, doResult, now string) (*SuggestedTask, error) {
	if deps.LLM == nil || strings.TrimSpace(doResult) == "" {
		return nil, nil
	}
	const prompt = `你是用户的openyou。以下是【Star】刚刚给出的行动建议全文。请把其中"当前最该做的一件事"提炼成一条结构化任务建议，供用户一键接受后创建任务。

只输出 JSON，不要任何其他文字。格式：
{"title":"<任务标题，不超过20字，动词开头>","description":"<一句话简要任务描述，不超过40字，说明做什么>","end_date":"<YYYY-MM-DD，空字符串表示今天>","priority":"<high|normal|low>"}

要求：
- title 用动词开头（如"跟进审批"），是用户能一眼看懂的一句话任务
- description 是简要描述，不是复刻全文
- end_date 根据建议里的时间信息推断；没有明确时间就留空
- priority 根据建议里的紧迫程度判断
- 如果建议里没有值得固化的任务，输出 {"title":"","description":"","end_date":"","priority":""}`

	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	raw, err := deps.LLM.Chat(callCtx, prompt, "建议全文：\n"+doResult+"\n\n当前时间："+now)
	if err != nil {
		return nil, err
	}
	var s SuggestedTask
	cleaned := strings.TrimSpace(raw)
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	cleaned = strings.TrimSpace(cleaned)
	if err := json.Unmarshal([]byte(cleaned), &s); err != nil {
		return nil, err
	}
	if strings.TrimSpace(s.Title) == "" {
		return nil, nil // LLM decided nothing is worth fixing
	}
	if s.Priority == "" {
		s.Priority = "normal"
	}
	return &s, nil
}

// buildDoPrompt: people names are masked upstream by the pseudonymizer.
func buildDoPrompt(deps Deps, now string) string {
	var b strings.Builder
	b.WriteString("你是用户的openyou。现在是 " + now + "。他刚点了【Star】——想让你一键替他把当下安排好。\n\n")
	b.WriteString("你和普通 AI 的根本区别：**你不是在给通用建议，你是在查他的过去**。\n")
	b.WriteString("你心里装着他的每个人物底细、每条用血泪换来的规则、每件还悬着的事、他重复犯的模式。\n")
	b.WriteString("每一句话都必须有出处——来自档案 [👤]、规则 [📋]、技能 [🧠]、待办 [✅]、记忆 [🧠]，")
	b.WriteString("而不是你自己编的。查不到根，就说'这是我的推测'。\n\n")

	b.WriteString("输出分为三大块：\n\n")
	b.WriteString("**① 行动建议**：结合当前时间、任务清单、规则和人物档案，按优先级给出行动计划。每件事包含：\n")
	b.WriteString("  - 做什么 (What)：可立刻执行的一句话动作\n")
	b.WriteString("  - 为什么现在 (Why)：结合截止/时机/规则说明优先级，必须引用来源 [📋规则#N] [👤人名]\n")
	b.WriteString("  - 怎么做好 (How)：切入点、关键话术、高效路径。涉及人物必须基于档案给出针对策略\n")
	b.WriteString("  - 避坑 (Pitfalls)：从待办 [✅] 和规则 [📋] 里找风险，没有再给经验推测并注明\n")
	b.WriteString("  - 思维工具 (Mental Model)：优先用他规则里已有的框架\n\n")
	b.WriteString("**② 关注项（待办/到期）**：基于任务清单 [✅] 数据，指出当前最该盯住的 2-5 个风险/机会点。\n")
	b.WriteString("  格式：每条列出【关注项】+【为什么现在重要】+【建议动作】。\n")
	b.WriteString("  任务里没有的就不要编。\n\n")
	b.WriteString("**③ 盲区洞察（重复模式）**：基于已确认的记忆 [🧠]，指出他可能看不到的重复模式——\n")
	b.WriteString("  情绪触发、决策偏差、人际互动规律。每个洞察必须引用具体记忆作为依据。\n")
	b.WriteString("  没有足够数据支撑就说'暂无足够数据发现盲区'，不要编造。\n\n")
	b.WriteString("**总原则**：直接克制可执行。已完成不再建议。条件不成熟说明缺条件。没有数据支撑的话不说。\n\n")

	var actionable strings.Builder

	if deps.Tasks != nil {
		if tasks, err := deps.Tasks.List(); err == nil && len(tasks) > 0 {
			actionable.WriteString("## 任务清单\n")
			for _, t := range tasks {
				status := "[ ]"
				if t.Done {
					status = "[x]"
				}
				line := "- " + status + " " + t.Title
				if t.EndDate != "" {
					line += "（截止 " + t.EndDate + "）"
				}
				actionable.WriteString(line + "\n")
			}
			actionable.WriteString("\n")
		}
	}

	if deps.Memory != nil {
		if entries, err := deps.Memory.ListConfirmed(); err == nil && len(entries) > 0 {
			actionable.WriteString("## 盲区洞察（已确认的记忆）\n")
			for _, e := range entries {
				actionable.WriteString("- " + truncateRunes(e.Content, 120) + "\n")
			}
			actionable.WriteString("\n")
		}
	}

	b.WriteString(actionable.String())

	rules := ""
	if deps.Skills != nil {
		if r, err := deps.Skills.ReadDir("rule"); err == nil {
			rules = r
		}
	}
	if rules != "" {
		b.WriteString(memCtxOpen + "\n相关规则:\n" + rules + "\n" + memCtxClose + "\n")
	}

	return b.String()
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

func relevantProfiles(deps Deps, text string) string {
	if deps.Skills == nil {
		return ""
	}
	c, err := deps.Skills.ReadDir("inside")
	if err != nil || strings.TrimSpace(c) == "" {
		return ""
	}
	var b strings.Builder
	lines := strings.Split(c, "\n")
	var currentName string
	var currentContent strings.Builder
	inPerson := false

	flush := func() {
		if currentName != "" && matchesProfile(text, currentName) {
			b.WriteString("### " + currentName + "\n" + strings.TrimSpace(currentContent.String()) + "\n\n")
		}
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "# ") && !strings.HasPrefix(line, "## ") {
			flush()
			currentName = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			currentContent.Reset()
			inPerson = true
		} else if inPerson && strings.HasPrefix(line, "# ") {
			flush()
			currentName = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			currentContent.Reset()
		} else if inPerson && currentName != "" {
			currentContent.WriteString(line + "\n")
		}
	}
	flush()
	return b.String()
}

// matchesProfile matches a name at three leniency levels: exact, surname, and
// given-name — so "Boss Wang" or "Wei" match "Wang Wei".
func matchesProfile(text, name string) bool {
	textLower := strings.ToLower(text)
	nameLower := strings.ToLower(name)
	if strings.Contains(textLower, nameLower) {
		return true
	}
	runes := []rune(name)
	// Surname match — only for names ≥3 runes to avoid over-matching single chars.
	if len(runes) >= 3 && strings.Contains(textLower, strings.ToLower(string(runes[:2]))) {
		return true
	}
	// Given name — characters after the first, covers two-character given names.
	if len(runes) >= 3 && strings.Contains(textLower, strings.ToLower(string(runes[1:]))) {
		return true
	}
	return false
}
