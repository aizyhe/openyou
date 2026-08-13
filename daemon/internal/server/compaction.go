package server

import (
	"context"
	"log"
	"strings"
	"time"

	"openyou/daemon/internal/ai/llm"
	"openyou/daemon/internal/store/consult"
)

// Context-window management: keeps recent turns verbatim, summarizes the oldest
// once the backlog grows. Token counts are estimated (CJK ≈1.5 chars/token,
// ASCII ≈4), not tokenized.
const (
	// tokenSafetyPct inflates estimates to guard against under-counting.
	tokenSafetyPct = 15
	// compactThreshold: summarize older turns above this — sized for a
	// conservative model window with room left for the reply + summary call.
	compactThreshold = 24000
	// recentMinTurns / recentMaxTokens bound the verbatim tail: always keep at
	// least N recent turns, but never let the verbatim buffer itself exceed a
	// token budget (one long turn shouldn't blow the window).
	recentMinTurns  = 8
	recentMaxTokens = 8000
	recentTurnsKept = recentMinTurns // kept for existing tests / callers
)

func estimateTokens(s string) int {
	// Count CJK and ASCII separately: CJK ≈ 1.5 chars/token, ASCII ≈ 4 chars/token.
	var cjk, ascii int
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF || r >= 0x3000 && r <= 0x303F || r >= 0xFF00 && r <= 0xFFEF {
			cjk++
		} else {
			ascii++
		}
	}
	base := cjk*2/3 + ascii/4 + 1 // floor division; +1 prevents zero
	return base + base*tokenSafetyPct/100 + 1
}

// recentSplit bounds the verbatim tail by a hard token ceiling so a few huge
// turns can never bypass compaction; recentMinTurns is a soft floor honored
// only when it also fits the ceiling. The newest turn is always kept.
func recentSplit(msgs []llm.Message) int {
	if len(msgs) <= 1 {
		return 0
	}
	tokens := 0
	keep := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		t := estimateTokens(msgs[i].Content)
		// Always keep the newest turn; stop once an older turn would exceed the ceiling.
		if keep >= 1 && tokens+t > recentMaxTokens {
			return i + 1 // older turns (0..i) get summarized
		}
		tokens += t
		keep++
		// Stop once we've kept the soft floor and are at/over budget.
		if keep >= recentMinTurns && tokens >= recentMaxTokens {
			return i // summarize everything strictly older than i
		}
	}
	return 0 // whole history fits the tail — nothing to summarize
}

// compactHistory returns [summary?] + recent verbatim turns; stored rows are untouched.
func compactHistory(ctx context.Context, llmc ChatStreamer, prior []*consult.Message) []llm.Message {
	// Convert to llm.Message, dropping any stored system rows.
	msgs := make([]llm.Message, 0, len(prior))
	for _, m := range prior {
		if m.Role == "system" {
			continue
		}
		msgs = append(msgs, llm.Message{Role: m.Role, Content: m.Content})
	}

	total := 0
	for _, m := range msgs {
		total += estimateTokens(m.Content)
	}
	// Compact on total size alone — a few very long turns can overflow the window.
	if total <= compactThreshold {
		return msgs // small enough — send as-is
	}
	if len(msgs) <= 1 {
		return msgs // can't summarize a single (current) turn away
	}

	split := recentSplit(msgs)
	if split == 0 {
		return msgs
	}
	old := msgs[:split]
	recent := msgs[split:]

	summary := summarizeTurns(ctx, llmc, old)
	if summary == "" {
		// Summarization failed — hard-trim to recent turns so we still don't overflow.
		return recent
	}
	out := make([]llm.Message, 0, len(recent)+1)
	out = append(out, llm.Message{
		Role:    "system",
		Content: "【早期对话摘要（供你记住上下文，非用户新输入）】\n" + summary,
	})
	out = append(out, recent...)
	return out
}

// summarizeTurns compresses old turns into a factual summary preserving
// citation-worthy facts (people, rules, decisions, preferences). A 30s timeout
// guards against LLM latency; on error the caller falls back to the recent tail.
func summarizeTurns(ctx context.Context, llmc ChatStreamer, old []llm.Message) string {
	if llmc == nil || len(old) == 0 {
		return ""
	}
	var b strings.Builder
	for _, m := range old {
		role := "用户"
		if m.Role == "assistant" {
			role = "你"
		}
		b.WriteString(role + ": " + m.Content + "\n")
	}
	sys := "你在压缩一段较早的对话历史，供后续对话作为记忆参考。" +
		"用简洁要点保留：用户的姓名/身份/偏好/目标、提到的人物及其关键信息、" +
		"引用过的规则、你给出的关键建议与用户的反馈、尚未解决的问题。" +
		"不要寒暄，不要复述无关细节，只输出要点。"
	summarizeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := llmc.Chat(summarizeCtx, sys, b.String())
	if err != nil {
		log.Printf("compaction: summarizeTurns failed (%d turns): %v — falling back to recent turns", len(old), err)
		return ""
	}
	return strings.TrimSpace(out)
}
