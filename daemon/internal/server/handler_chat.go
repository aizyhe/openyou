package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"openyou/daemon/internal/ai/llm"
	"openyou/daemon/internal/ai/thinktank"
	"openyou/daemon/internal/store/memory"
)

func itoaSafe(n int) string { return strconv.Itoa(n) }

// stablePrefixCache caches the system prefix by TTL — avoids per-request DB round-trips.
var (
	stablePrefixCacheMu   sync.Mutex
	stablePrefixCached    string
	stablePrefixLastBuilt time.Time
	stablePrefixTTL       = 5 * time.Minute
)

func InvalidateStablePrefix() {
	stablePrefixCacheMu.Lock()
	stablePrefixLastBuilt = time.Time{}
	stablePrefixCacheMu.Unlock()
}

// sanitizeLLMError strips internal path prefixes and truncates so internals don't leak to the client.
func sanitizeLLMError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if i := strings.Index(s, ": "); i >= 0 && i < 30 {
		s = s[i+2:]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

type chatBody struct {
	Message   string `json:"message"`
	SessionID string `json:"session_id"`
}

type sseWriter struct {
	w  http.ResponseWriter
	fl http.Flusher
}

func newSSE(w http.ResponseWriter) (*sseWriter, bool) {
	fl, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	return &sseWriter{w: w, fl: fl}, true
}

func (s *sseWriter) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	fmt.Fprintf(s.w, "data: %s\n\n", b)
	s.fl.Flush()
}

func (s *sseWriter) sendRaw(line string) {
	fmt.Fprintf(s.w, "data: %s\n\n", line)
	s.fl.Flush()
}

// sendEvent emits a named SSE event for structured side-channel payloads (e.g.
// suggest-task) the client handles distinctly from text deltas.
func (s *sseWriter) sendEvent(name string, payload []byte) {
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, payload)
	s.fl.Flush()
}

// buildHistory: stable (cacheable) prefix first, volatile note separate, so the
// volatile text can't break prefix-cache hits. Long sessions are compacted.
func buildHistory(ctx context.Context, deps Deps, sessionID, stablePrefix, volatile string) []llm.Message {
	msgs := make([]llm.Message, 0, 16)
	if stablePrefix != "" {
		msgs = append(msgs, llm.Message{Role: "system", Content: stablePrefix})
	}
	if volatile != "" {
		msgs = append(msgs, llm.Message{Role: "system", Content: volatile})
	}
	if deps.Sessions == nil || sessionID == "" {
		return msgs
	}
	prior, err := deps.Sessions.GetMessages(sessionID)
	if err != nil {
		return msgs
	}
	msgs = append(msgs, compactHistory(ctx, deps.LLM, prior)...)
	return msgs
}

func handleChat(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var body chatBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if strings.TrimSpace(body.Message) == "" {
			writeErr(w, http.StatusBadRequest, "empty message")
			return
		}
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

		sse, ok := newSSE(w)
		if !ok {
			writeErr(w, http.StatusInternalServerError, "streaming unsupported")
			return
		}

		sessionID := ensureSession(deps, body.SessionID)
		sse.send(map[string]string{"type": "session", "session_id": sessionID})
		sendContextLoaded(deps, sse)

		// Persist the user turn BEFORE building history so it's included.
		if deps.Sessions != nil {
			_, _ = deps.Sessions.AppendMessage(sessionID, "user", body.Message)
		}

		ctx := r.Context()
		// Cache-friendly layout: stable prefix first (byte-identical across turns),
		// volatile time-layer data and spotlight in a separate later message so
		// they don't bust the prefix cache.
		history := buildHistory(ctx, deps, sessionID, stableSystemPrefix(deps),
			followUpReminder(deps)+spotlightContext(deps, body.Message))
		scrub := &streamScrubber{}
		full, err := deps.LLM.Stream(ctx, history, func(delta string) {
			if safe := scrub.push(delta); safe != "" {
				sse.send(map[string]string{"type": "text-delta", "text": safe})
			}
		})
		if tail := scrub.flush(); tail != "" {
			sse.send(map[string]string{"type": "text-delta", "text": tail})
		}
		if err != nil {
			// Persist the partial reply so the user turn doesn't dangle unanswered; scrub internal markers.
			if partial := scrubFinal(full); partial != "" && deps.Sessions != nil {
				_, _ = deps.Sessions.AppendMessage(sessionID, "assistant", partial)
			}
			sse.send(map[string]string{"type": "error", "error": sanitizeLLMError(err)})
			sse.sendRaw("[DONE]")
			return
		}

		// Persist scrubbed text so history never carries markers, including unclosed ones.
		full = scrubFinal(full)

		if deps.Sessions != nil {
			_, _ = deps.Sessions.AppendMessage(sessionID, "assistant", full)
		}
		enqueueMemory(deps, sessionID)

		emitCitations(sse, full)

		sse.send(map[string]string{"type": "done"})
		sse.sendRaw("[DONE]")
	}
}

func handleAnalyze(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var body chatBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if strings.TrimSpace(body.Message) == "" {
			writeErr(w, http.StatusBadRequest, "empty message")
			return
		}
		if deps.BrainTrust == nil {
			writeErr(w, http.StatusServiceUnavailable, "brain trust not configured")
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

		sse, ok := newSSE(w)
		if !ok {
			writeErr(w, http.StatusInternalServerError, "streaming unsupported")
			return
		}

		sessionID := ensureSession(deps, body.SessionID)
		sse.send(map[string]string{"type": "session", "session_id": sessionID})
		sendContextLoaded(deps, sse)

		// Persist user turn.
		if deps.Sessions != nil {
			_, _ = deps.Sessions.AppendMessage(sessionID, "user", body.Message)
		}

		ctx := r.Context()
		// Prepend prior turns so the brain trust and synthesis see the running conversation.
		query := buildContextualQuery(ctx, deps, sessionID, body.Message)
		events, err := deps.BrainTrust.Analyze(ctx, query, sessionID)
		if err != nil {
			if deps.Sessions != nil {
				_, _ = deps.Sessions.AppendMessage(sessionID, "assistant", "[分析出错]")
			}
			sse.send(map[string]string{"type": "error", "error": sanitizeLLMError(err)})
			sse.sendRaw("[DONE]")
			return
		}

		var reply strings.Builder
		scrub := &streamScrubber{}
		sawError := false
		for ev := range events {
			if ev.Type == thinktank.EventSynthesisDelta {
				// Scrub internal markers before synthesis reaches the client (same as handleChat).
				if safe := scrub.push(ev.Text); safe != "" {
					out := ev
					out.Text = safe
					sse.send(out)
					reply.WriteString(safe)
				}
				continue
			}
			// Skill results are raw LLM output that may contain <think>…</think>; scrub before sending.
			if ev.Type == thinktank.EventSkillResult {
				out := ev
				out.Result = scrubFinal(ev.Result)
				sse.send(out)
				continue
			}
			if ev.Type == thinktank.EventError {
				sawError = true
			}
			// Drop thinktank's own EventDone — the handler sends the canonical done + [DONE].
			if ev.Type == thinktank.EventDone {
				continue
			}
			sse.send(ev)
		}
		if tail := scrub.flush(); tail != "" {
			sse.send(thinktank.AnalysisEvent{Type: thinktank.EventSynthesisDelta, Text: tail})
			reply.WriteString(tail)
		}

		if deps.Sessions != nil {
			if reply.Len() > 0 {
				_, _ = deps.Sessions.AppendMessage(sessionID, "assistant", reply.String())
			} else if sawError {
				// Persist a placeholder so the user turn doesn't dangle unanswered.
				_, _ = deps.Sessions.AppendMessage(sessionID, "assistant", "[分析出错]")
			}
		}
		enqueueMemory(deps, sessionID)

		emitCitations(sse, reply.String())

		sse.send(map[string]string{"type": "done"})
		sse.sendRaw("[DONE]")
	}
}

func ensureSession(deps Deps, id string) string {
	if id != "" {
		return id
	}
	if deps.Sessions != nil {
		if s, err := deps.Sessions.Create(""); err == nil {
			return s.ID
		}
	}
	// Fall back to a unique ephemeral ID so concurrent requests never share a session.
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		log.Printf("ensureSession: rand.Read: %v", err)
		// Fallback: use timestamp as ID.
		return fmt.Sprintf("ephemeral_%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("ephemeral_%x", b)
}

// sendContextLoaded: field names match web/index.html handleContextLoaded.
// TODO: batch inside fetches — ReadDir is called 3x per request.
func sendContextLoaded(deps Deps, sse *sseWriter) {
	data := map[string]any{"type": "context-loaded"}
	var names []string
	if deps.Skills != nil {
		if c, err := deps.Skills.ReadDir("inside"); err == nil {
			n := 0
			for _, line := range strings.Split(c, "\n") {
				if strings.HasPrefix(line, "# ") {
					n++
					name := strings.TrimPrefix(line, "# ")
					names = append(names, name)
				}
			}
			data["people_count"] = n
		}
		if rules, err := deps.Skills.ReadDir("rule"); err == nil {
			n := strings.Count(rules, "\n#")
			if strings.HasPrefix(strings.TrimSpace(rules), "#") {
				n++
			}
			data["rules_count"] = n
		}
	}
	if deps.Memory != nil {
		if n, err := deps.Memory.Count(); err == nil {
			data["memory_count"] = n
		}
	}
	data["people_names"] = names
	sse.send(data)
}

// buildContextualQuery: a single-string brain-trust query still carries history
// continuity; oldest turns are compacted to stay within context limits.
func buildContextualQuery(ctx context.Context, deps Deps, sessionID, latest string) string {
	if deps.Sessions == nil || sessionID == "" {
		return latest
	}
	prior, err := deps.Sessions.GetMessages(sessionID)
	if err != nil || len(prior) <= 1 {
		return latest
	}
	// Drop the just-appended latest user turn (last element) before compacting.
	history := compactHistory(ctx, deps.LLM, prior[:len(prior)-1])
	if len(history) == 0 {
		return latest
	}
	var b strings.Builder
	b.WriteString("对话历史（供你记住上下文，如用户姓名、此前建议等）:\n")
	for _, m := range history {
		role := "用户"
		if m.Role == "assistant" {
			role = "你"
		} else if m.Role == "system" {
			role = "摘要"
		}
		b.WriteString(role + ": " + m.Content + "\n")
	}
	b.WriteString("\n当前问题: " + latest)
	return b.String()
}

// stableSystemPrefix: byte-identical (cacheable) prefix — Rules in full. Tasks
// change every turn and belong in the volatile layer. Cached for stablePrefixTTL.
func stableSystemPrefix(deps Deps) string {
	stablePrefixCacheMu.Lock()
	if time.Since(stablePrefixLastBuilt) < stablePrefixTTL && stablePrefixCached != "" {
		stablePrefixCacheMu.Unlock()
		return stablePrefixCached
	}
	// Build outside the lock so concurrent requests aren't serialized behind the disk walk.
	stablePrefixCacheMu.Unlock()

	result := buildStableSystemPrefix(deps)

	stablePrefixCacheMu.Lock()
	// Re-check under lock: another goroutine may have built it while we were doing I/O.
	if time.Since(stablePrefixLastBuilt) >= stablePrefixTTL || stablePrefixCached == "" {
		stablePrefixCached = result
		stablePrefixLastBuilt = time.Now()
	}
	stablePrefixCacheMu.Unlock()
	return result
}

func buildStableSystemPrefix(deps Deps) string {
	var b strings.Builder
	b.WriteString("你是用户的openyou——平行版本的他，掌握他的完整知识库。\n")
	b.WriteString("基于他的档案、规则、历史给出有理有据、直接克制的建议，而不是泛泛而谈。\n")
	b.WriteString("引用来源时用标记：[👤人名] [📋规则#N] [🧠技能名] [🌐外部搜索]。\n")
	b.WriteString("查不到根，就说'这是我的推测'，不假装成你的历史。\n")

	rules := ""
	if deps.Skills != nil {
		if r, err := deps.Skills.ReadDir("rule"); err == nil {
			rules = r
		}
	}
	if strings.TrimSpace(rules) != "" {
		b.WriteString("\n" + memCtxOpen + "\n")
		b.WriteString("## 规则（行为准则与经验总结）\n")
		b.WriteString(strings.TrimSpace(rules) + "\n")
		b.WriteString(memCtxClose + "\n")
	}

	// inside docs are NOT injected here — the tree is huge, so they're retrieved
	// on demand per message in spotlightContext(); the stable prefix stays small.
	return b.String()
}

// spotlightContext builds the volatile per-request fragment (people mentioned +
// incomplete Tasks) — dynamic data that changes every turn, so it stays out of the stable prefix.
func spotlightContext(deps Deps, message string) string {
	var b strings.Builder

	if deps.Tasks != nil {
		if tasks, err := deps.Tasks.List(); err == nil {
			var pending []string
			for _, t := range tasks {
				if !t.Done {
					line := "• " + t.Title
					if t.EndDate != "" {
						line += "（截止 " + t.EndDate + "）"
					}
					pending = append(pending, line)
				}
			}
			if len(pending) > 0 {
				b.WriteString("\n\n## 待办任务（未完成）\n")
				for _, p := range pending {
					b.WriteString(p + "\n")
				}
			}
		}
	}

	// Only docs relevant to this message are injected, within a budget; the full
	// inside/ tree is never force-loaded. Content-based, not filename-based.
	if deps.Skills != nil {
		if relevant := deps.Skills.RelevantDocs(message, chatInsideBudget); len(relevant) > 0 {
			b.WriteString("\n\n## 相关档案（按本次问题检索）\n")
			for _, chunk := range relevant {
				b.WriteString(chunk + "\n")
			}
		}
		// List a bounded sample so openyou knows what it can read on demand even when nothing matched.
		if docs := deps.Skills.ListDocs(); len(docs) > 0 {
			var insideDocs []string
			for _, d := range docs {
				if strings.HasPrefix(d, "inside/") {
					insideDocs = append(insideDocs, d)
				}
			}
			if len(insideDocs) > 0 {
				b.WriteString("\n\n## 可用档案清单（需要时按名读取）\n")
				n := len(insideDocs)
				limit := chatDocsListed
				if n > limit {
					for i := 0; i < limit/2; i++ {
						b.WriteString("- " + insideDocs[i] + "\n")
					}
					b.WriteString(fmt.Sprintf("…(共 %d 个档案，仅显示前 %d 个)\n", n, limit/2))
				} else {
					for _, d := range insideDocs {
						b.WriteString("- " + d + "\n")
					}
				}
			}
		}
	}

	return b.String()
}

// chatInsideBudget caps how much inside/ content is retrieved per chat message.
// Rules load in full (constitutional); inside docs are on-demand and bounded.
const chatInsideBudget = 20000

// chatDocsListed caps the inside doc index sample per chat to keep the prompt lean.
const chatDocsListed = 40

// followUpReminder nudges openyou to ask how earlier, un-followed-up advice
// turned out. Empty if nothing is pending.
func followUpReminder(deps Deps) string {
	pending := computePendingFeedback(deps, 3)
	if len(pending) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【主动追问】以下会话你此前给过建议但用户还没反馈结果，" +
		"若话题相关，自然地问一句进展（不要生硬罗列）：")
	for i, p := range pending {
		if i >= 3 {
			break
		}
		title := p.Title
		if title == "" {
			title = "(无标题会话)"
		}
		b.WriteString("\n- " + title + "（" + itoaSafe(p.Days) + " 天前）")
	}
	return b.String()
}

func enqueueMemory(deps Deps, sessionID string) {
	if deps.MemoryQueue == nil || deps.Sessions == nil {
		return
	}
	msgs, err := deps.Sessions.GetMessages(sessionID)
	if err != nil {
		return
	}
	// Cost control: extract only every 3rd assistant turn; the full conversation is still passed.
	assistantTurns := 0
	for _, m := range msgs {
		if m.Role == "assistant" {
			assistantTurns++
		}
	}
	if assistantTurns == 0 || assistantTurns%3 != 0 {
		return
	}
	// Skip very short conversations here to avoid useless queue entries (extractor has the same guard).
	var convo strings.Builder
	for _, m := range msgs {
		convo.WriteString(m.Role + ": " + m.Content + "\n")
	}
	convoText := convo.String()
	if !memory.HasExtractableLength(convoText) {
		return
	}
	deps.MemoryQueue.Enqueue(memory.ExtractJob{
		SessionID:        sessionID,
		ConversationText: convoText,
	})
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
