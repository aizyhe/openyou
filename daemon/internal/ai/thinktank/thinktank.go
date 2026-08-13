package thinktank

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	maxSubAgents    = 5
	subAgentTimeout = 60 * time.Second
)

type ThinkTank struct {
	llm    LLMClient
	skills SkillProvider
	memory MemoryProvider // optional; recalls relevant memories into context
	docs   DocProvider    // optional; used to inject a skill's declared deps
}

func New(llm LLMClient, skills SkillProvider, mem MemoryProvider, docs DocProvider) *ThinkTank {
	return &ThinkTank{llm: llm, skills: skills, memory: mem, docs: docs}
}

func (bt *ThinkTank) Analyze(ctx context.Context, query, _ string) (<-chan AnalysisEvent, error) {
	matched := bt.skills.Match(query)
	ch := make(chan AnalysisEvent, 32)

	go func() {
		defer close(ch)

		if len(matched) == 0 {
			// No skills matched — fall back to simple analysis.
			result, err := bt.llm.Chat(ctx, bt.buildSimplePrompt(query), query)
			if err != nil {
				emit(ctx, ch, AnalysisEvent{Type: EventError, Error: err.Error()})
				return
			}
			emit(ctx, ch, AnalysisEvent{Type: EventSynthesisDelta, Text: result})
			emit(ctx, ch, AnalysisEvent{Type: EventDone})
			return
		}

		// 1 skill: deep analysis without thinktank overhead, emitting only
		// synthesis-delta — also emitting skill-result made the client append
		// the text twice while the server persisted it once.
		if len(matched) == 1 {
			result, err := bt.runSubAgent(ctx, matched[0], query)
			if err != nil {
				emit(ctx, ch, AnalysisEvent{Type: EventError, Error: err.Error()})
				return
			}
			emit(ctx, ch, AnalysisEvent{Type: EventSynthesisDelta, Text: result})
			emit(ctx, ch, AnalysisEvent{Type: EventDone})
			return
		}

		// 2+ skills: thinktank mode.
		skills := matched
		if len(skills) > maxSubAgents {
			skills = skills[:maxSubAgents]
		}

		briefs := make([]SkillBrief, len(skills))
		for i, s := range skills {
			briefs[i] = SkillBrief{Name: s.Name, Description: s.Description}
		}
		emit(ctx, ch, AnalysisEvent{Type: EventThinkTankStart, Skills: briefs})

		sem := make(chan struct{}, maxSubAgents)
		results := make(chan subResult, len(skills))

		var wg sync.WaitGroup
		for _, sk := range skills {
			wg.Add(1)
			go func(sk SkillInfo) {
				defer wg.Done()
				// Acquire the concurrency slot, but abort if the caller is gone.
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
					return
				}

				// Progress events use emit so a stopped consumer can't wedge us.
				emit(ctx, ch, AnalysisEvent{Type: EventSkillProgress, Skill: sk.Name, Status: "analyzing"})

				ctx2, cancel := context.WithTimeout(ctx, subAgentTimeout)
				defer cancel()

				r, err := bt.runSubAgent(ctx2, sk, query)
				if err != nil {
					emit(ctx, ch, AnalysisEvent{Type: EventSkillProgress, Skill: sk.Name, Status: "failed", Error: err.Error()})
					results <- subResult{skill: sk.Name, err: err} // buffered: never blocks
					return
				}
				emit(ctx, ch, AnalysisEvent{Type: EventSkillProgress, Skill: sk.Name, Status: "complete"})
				results <- subResult{skill: sk.Name, result: r} // buffered: never blocks
			}(sk)
		}
		wg.Wait()
		close(results)

		// If the caller went away, stop now — nothing left to stream.
		if ctx.Err() != nil {
			return
		}

		var analyses []subResult
		for r := range results {
			analyses = append(analyses, r)
		}

		for _, a := range analyses {
			if a.err == nil {
				emit(ctx, ch, AnalysisEvent{Type: EventSkillResult, Skill: a.skill, Result: a.result})
			}
		}

		// Detect conflicts: different skills recommending opposite actions.
		conflicts := bt.detectConflicts(analyses)
		for _, c := range conflicts {
			emit(ctx, ch, AnalysisEvent{Type: EventConflict, SkillA: c.skillA, SkillB: c.skillB, Description: c.description})
		}

		validResults := make([]subResult, 0, maxSubAgents)
		for _, a := range analyses {
			if a.err == nil && a.result != "" {
				validResults = append(validResults, a)
			}
		}
		if len(validResults) == 0 {
			emit(ctx, ch, AnalysisEvent{Type: EventError, Error: "all sub-agents failed"})
			return
		}

		_, emitted, err := bt.synthesize(ctx, query, validResults, conflicts, ch)
		if err != nil {
			if emitted {
				// Partial synthesis already streamed; re-sending raw results
				// would duplicate content, so fail visibly instead.
				emit(ctx, ch, AnalysisEvent{Type: EventError, Error: "synthesis interrupted: " + err.Error()})
				emit(ctx, ch, AnalysisEvent{Type: EventDone})
				return
			}
			// Nothing streamed yet — fall back to individual results.
			for _, a := range validResults {
				for _, chunk := range splitChunks(a.result, 200) {
					emit(ctx, ch, AnalysisEvent{Type: EventSynthesisDelta, Text: fmt.Sprintf("[%s]\n%s", a.skill, chunk)})
				}
			}
			emit(ctx, ch, AnalysisEvent{Type: EventDone})
			return
		}

		// Synthesis was streamed as deltas in synthesize() — just mark done.
		emit(ctx, ch, AnalysisEvent{Type: EventDone})
	}()

	return ch, nil
}

func (bt *ThinkTank) runSubAgent(ctx context.Context, sk SkillInfo, query string) (string, error) {
	prompt := bt.buildSkillPrompt(sk, query)
	return bt.llm.Chat(ctx, prompt, query)
}

// skillCharBudget caps the framework body injected for a matched skill, so one
// oversized skill doc can't blow a sub-agent's context window on its own.
const skillCharBudget = 12000

func (bt *ThinkTank) buildSkillPrompt(sk SkillInfo, query string) string {
	var b strings.Builder

	// Build once so the inside/ branch below can decide whether on-demand retrieval matched.
	insideContext := bt.buildInsideContext(query)

	// An inside/ doc matched as a "skill" must not have its whole body injected
	// here — buildInsideContext already retrieves the relevant sections on
	// demand, so injecting again would duplicate content and blow the window.
	if strings.HasPrefix(sk.Path, "inside/") {
		b.WriteString("You are an expert analyst. Base your analysis on the relevant archive sections retrieved below — do not rely on generic advice.\n\n---\n\n")
		// If on-demand retrieval matched nothing, inject the full archive
		// (clipped) so the prompt is not hollow.
		if strings.TrimSpace(insideContext) == "" {
			b.WriteString("相关档案按需检索未命中，注入其全文内容供分析：\n\n")
			b.WriteString(clipRunes(sk.Content, skillCharBudget))
			b.WriteString("\n\n---\n\n")
		}
	} else {
		b.WriteString("You are an expert analyst using the following framework:\n\n")
		b.WriteString(clipRunes(sk.Content, skillCharBudget))
		b.WriteString("\n\n---\n\n")
	}

	// Inject the skill's declared dependency files (frontmatter `deps:`) so the
	// model sees their content — it has no tool to open files. Bounded by budget.
	if deps := bt.buildDepsContext(sk); deps != "" {
		b.WriteString("Reference documents this skill depends on:\n\n")
		b.WriteString(deps)
		b.WriteString("\n\n---\n\n")
	}

	if insideContext != "" {
		b.WriteString("Relevant context about the people and rules involved:\n\n")
		b.WriteString(insideContext)
		b.WriteString("\n\n---\n\n")
	}

	b.WriteString("Analyze the following situation using ONLY the framework above and the context provided. ")
	b.WriteString("Be specific, actionable, and cite which person profiles or rules support your analysis. ")
	b.WriteString("Use citation markers to tag your sources: [👤人名] for people, [📋规则#N] for rules, ")
	b.WriteString("[🧠技能名] for skills, [🌐外部搜索] for external sources. ")
	b.WriteString("DO NOT give generic advice. Base everything on the provided data.")
	return b.String()
}

// clipRunes truncates s to at most max runes with a marker — the size guard
// for skill bodies and other non-chunked injections.
func clipRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= max {
			b.WriteString("\n…(truncated)")
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// depsCharBudget caps the total characters of dependency-file content injected
// into a single sub-agent prompt, so a skill declaring several large deps can't
// blow past the model's context window.
const depsCharBudget = 12000

func (bt *ThinkTank) buildDepsContext(sk SkillInfo) string {
	if bt.docs == nil || len(sk.Deps) == 0 {
		return ""
	}
	var b strings.Builder
	remaining := depsCharBudget
	for _, dep := range sk.Deps {
		if remaining <= 0 {
			break
		}
		content := bt.readDep(dep)
		if strings.TrimSpace(content) == "" {
			continue
		}
		if len(content) > remaining {
			// Back off to a rune boundary to avoid splitting multi-byte characters.
			cutAt := remaining
			for cutAt > 0 && !utf8.RuneStart(content[cutAt]) {
				cutAt--
			}
			content = content[:cutAt] + "\n…(truncated)"
		}
		remaining -= len(content)
		b.WriteString("### " + dep + "\n\n")
		b.WriteString(strings.TrimSpace(content))
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

func (bt *ThinkTank) readDep(dep string) string {
	dep = strings.TrimSpace(dep)
	if !strings.HasSuffix(dep, ".md") && bt.docs != nil {
		if c, err := bt.docs.ReadDir(dep); err == nil && strings.TrimSpace(c) != "" {
			return c
		}
		if trimmed := strings.TrimPrefix(dep, "context/"); trimmed != dep {
			if c, err := bt.docs.ReadDir(trimmed); err == nil && strings.TrimSpace(c) != "" {
				return c
			}
		}
		return ""
	}
	if c, err := bt.docs.ReadDoc(dep); err == nil {
		return c
	}
	if trimmed := strings.TrimPrefix(dep, "context/"); trimmed != dep {
		if c, err := bt.docs.ReadDoc(trimmed); err == nil {
			return c
		}
	}
	return ""
}

func (bt *ThinkTank) buildSimplePrompt(query string) string {
	var b strings.Builder
	b.WriteString("You are the user's openyou — a parallel version of them with access to their complete personal context. ")
	b.WriteString("Analyze the situation based on what you know, not on generic advice.")
	ctx := bt.buildInsideContext(query)
	if ctx != "" {
		b.WriteString("\n\nRelevant context:\n\n" + ctx)
	}
	return b.String()
}

// buildInsideContext: rules always, plus relevant inside docs and memories on demand.
func (bt *ThinkTank) buildInsideContext(query string) string {
	var b strings.Builder

	// Rules load unconditionally — few and dense, every answer must be
	// grounded in them, unlike the on-demand content below.
	rules := ""
	if bt.docs != nil {
		if r, err := bt.docs.ReadDir("rule"); err == nil {
			rules = r
		}
	}
	if rules != "" {
		b.WriteString("## 规则（必须遵守）\n\n")
		b.WriteString(rules)
		b.WriteString("\n\n")
	}

	// inside/ docs are retrieved on demand within a budget — injecting the whole
	// tree (resumes, QA libraries) would blow the context window.
	if bt.docs != nil {
		if relevant := bt.docs.RelevantDocs(query, insideCharBudget); len(relevant) > 0 {
			b.WriteString("## 相关档案（按本次问题检索）\n\n")
			for _, chunk := range relevant {
				b.WriteString(chunk)
				b.WriteString("\n\n")
			}
		}
	}

	if bt.memory != nil {
		if mems := bt.memory.Relevant(query, 5); len(mems) > 0 {
			b.WriteString("## 相关记忆\n\n")
			for _, m := range mems {
				b.WriteString("- " + m + "\n")
			}
			b.WriteString("\n")
		}
	}

	// Discoverable index, capped — listing thousands of paths would flood the
	// prompt, so show a sample plus the total.
	if bt.docs != nil {
		if docs := bt.docs.ListDocs(); len(docs) > 0 {
			b.WriteString("## 可用上下文文档（需要时按名读取）\n")
			n := len(docs)
			limit := maxDocsListed
			if n > limit {
				for i := 0; i < limit/2; i++ {
					b.WriteString("- " + docs[i] + "\n")
				}
				b.WriteString(fmt.Sprintf("…(共 %d 个文档，仅显示前 %d 个)\n", n, limit/2))
			} else {
				for _, d := range docs {
					b.WriteString("- " + d + "\n")
				}
			}
			b.WriteString("\n")
		}
	}

	return b.String()
}

// maxDocsListed caps how many doc paths are listed — a sample suffices to know
// the index shape; specific docs are read on demand.
const maxDocsListed = 40

// insideCharBudget caps on-demand inside/ content injected per query, so a huge
// QA library or resume can't blow the context window.
const insideCharBudget = 20000

// escalateKW are keywords that indicate an aggressive/escalating recommendation.
var escalateKW = []string{
	"escalate", "confront", "push", "aggressive", "immediate", "act now",
	"强硬", "直接对抗", "立即行动", "进攻", "主动出击",
	"正面冲突", "强势", "逼迫", "施压",
}

// waitKW are keywords that indicate a wait-and-observe recommendation.
var waitKW = []string{
	"wait", "patience", "observe", "hold back", "delay", "caution",
	"等待", "观望", "忍耐", "缓一缓", "先不要", "暂缓", "静观其变",
	"保守", "低调", "迂回", "蛰伏",
}

func (bt *ThinkTank) detectConflicts(analyses []subResult) []struct {
	skillA, skillB, description string
} {
	var escalate, wait []string
	for _, a := range analyses {
		if a.err != nil || a.result == "" {
			continue
		}
		lower := strings.ToLower(a.result)
		for _, kw := range escalateKW {
			if strings.Contains(lower, kw) {
				escalate = append(escalate, a.skill)
				break
			}
		}
		for _, kw := range waitKW {
			if strings.Contains(lower, kw) {
				wait = append(wait, a.skill)
				break
			}
		}
	}
	var conflicts []struct {
		skillA, skillB, description string
	}
	for _, e := range escalate {
		for _, w := range wait {
			if e != w {
				conflicts = append(conflicts, struct {
					skillA, skillB, description string
				}{
					skillA: e, skillB: w,
					description: fmt.Sprintf("%s recommends escalation while %s recommends waiting.", e, w),
				})
			}
		}
	}
	return conflicts
}

// synthesize merges sub-agent analyses via one LLM call, streaming deltas. The
// second return distinguishes failure-before-output (safe to fall back) from
// mid-stream failure.
func (bt *ThinkTank) synthesize(ctx context.Context, query string, results []subResult, conflicts []struct {
	skillA, skillB, description string
}, ch chan<- AnalysisEvent) (string, bool, error) {
	var b strings.Builder
	b.WriteString("You are the user's openyou — a strategist synthesizing advice from multiple expert analyses.\n\n")
	b.WriteString("User's question: " + query + "\n\n")

	b.WriteString("## Expert Analyses\n\n")
	for _, r := range results {
		b.WriteString(fmt.Sprintf("### %s\n%s\n\n", r.skill, r.result))
	}

	if len(conflicts) > 0 {
		b.WriteString("## Conflicts Detected\n")
		for _, c := range conflicts {
			b.WriteString(fmt.Sprintf("- %s\n", c.description))
		}
		b.WriteString("\nPlease acknowledge these conflicts and explain the trade-offs.\n\n")
	}

	b.WriteString("## Instructions\n")
	b.WriteString("Synthesize these analyses into a single, coherent recommendation. ")
	b.WriteString("1. Start with the most important actionable insight.\n")
	b.WriteString("2. Integrate complementary perspectives from different analyses.\n")
	b.WriteString("3. Flag any contradictions between analyses and explain the trade-offs.\n")
	b.WriteString("4. When citing insights from a specific skill analysis, tag it with [🧠技能名] ")
	b.WriteString("(e.g. [🧠职场谈判] or [🧠用户分析]) so the user knows which expert angle provided that insight. ")
	b.WriteString("Also use these citation markers for other sources:\n")
	b.WriteString("   - [👤人名] for person profiles\n")
	b.WriteString("   - [📋规则#N] for rules\n")
	b.WriteString("   - [🌐外部搜索] for external sources\n")
	b.WriteString("5. End with 2-3 concrete next steps.\n")
	b.WriteString("Be direct and evidence-based. No flattery.")

	emitted := false
	full, err := bt.llm.StreamChat(ctx, b.String(), "Synthesize these analyses into a single recommendation.", func(delta string) {
		emitted = true
		emit(ctx, ch, AnalysisEvent{Type: EventSynthesisDelta, Text: delta})
	})
	return full, emitted, err
}

func splitChunks(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}
	var chunks []string
	runes := []rune(text)
	for i := 0; i < len(runes); i += maxLen {
		end := i + maxLen
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[i:end]))
	}
	return chunks
}

type subResult struct {
	skill  string
	result string
	err    error
}

// emit aborts on ctx cancellation, so sub-agent goroutines never block forever
// on a consumer-less channel (would deadlock wg.Wait()).
func emit(ctx context.Context, ch chan<- AnalysisEvent, ev AnalysisEvent) bool {
	select {
	case ch <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}
