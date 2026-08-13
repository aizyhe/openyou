package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type LLMClient interface {
	Chat(ctx context.Context, systemPrompt, userMessage string) (string, error)
}

type LLMMemoryExtractor struct {
	llm LLMClient
	ctx context.Context // parent context; base for per-call timeouts
}

func NewLLMMemoryExtractor(ctx context.Context, llm LLMClient) *LLMMemoryExtractor {
	if ctx == nil {
		ctx = context.Background()
	}
	return &LLMMemoryExtractor{llm: llm, ctx: ctx}
}

const extractSystemPrompt = `You are a memory extraction system for openyou. Analyze the conversation below and extract structured memories as JSON.

Output a JSON array of memory entries. Each entry has:
- "type": one of "person_fact", "user_trait", "rule", "context"
- "person": the name of the person this memory is about (empty if not about a specific person)
- "content": the key fact or insight (one sentence, specific, actionable)
- "confidence": a number 0.0 to 1.0 indicating how confident you are this is worth remembering

Guidelines:
- person_fact: new information about a specific person ("Alice is now focused on cost control")
- user_trait: a pattern or preference of the user ("prefers to gather data before deciding")
- rule: a behavioral rule discovered from experience ("Friday messages get low response rates")
- context: current situational context ("preparing for Q3 review next week")

Confidence thresholds:
- 0.85+: certain, should be auto-saved
- 0.60-0.85: likely, should be confirmed with the user
- below 0.60: discard

Only extract genuinely new information. Do not extract obvious or trivial facts.
Output ONLY the JSON array, no other text. Example: [{"type":"person_fact","person":"Alice","content":"Alice changed her focus to cost control","confidence":0.9}]`

// extractMinRunes: runes, not Fields — Fields counts a whole Chinese sentence as
// 1-2 fields, so a word-based threshold was nearly unreachable for Chinese.
const extractMinRunes = 300

func HasExtractableLength(text string) bool {
	return len([]rune(text)) >= extractMinRunes
}

func (e *LLMMemoryExtractor) Extract(conversationText string) ([]MemoryEntry, error) {
	if e.llm == nil {
		return nil, nil
	}
	// Cost control: skip very short conversations
	if !HasExtractableLength(conversationText) {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(e.ctx, 30*time.Second)
	defer cancel()

	response, err := e.llm.Chat(ctx, extractSystemPrompt, conversationText)
	if err != nil {
		return nil, fmt.Errorf("memory extraction: %w", err)
	}

	return parseExtractionResponse(response)
}

func parseExtractionResponse(response string) ([]MemoryEntry, error) {
	response = strings.TrimSpace(response)
	if strings.HasPrefix(response, "```") {
		response = strings.TrimPrefix(response, "```json")
		response = strings.TrimPrefix(response, "```")
		response = strings.TrimSuffix(response, "```")
		response = strings.TrimSpace(response)
	}

	var rawEntries []struct {
		Type       string  `json:"type"`
		Person     string  `json:"person"`
		Content    string  `json:"content"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(response), &rawEntries); err != nil {
		return nil, fmt.Errorf("parse extraction response: %w (raw: %.200s)", err, response)
	}

	var entries []MemoryEntry
	for _, raw := range rawEntries {
		if raw.Content == "" || raw.Confidence < discardFloor {
			continue
		}
		entry := NewMemoryEntry(
			MemoryType(raw.Type),
			raw.Content,
			raw.Confidence,
			"", // sourceSession filled by caller
		)
		if raw.Person != "" {
			entry.Person = raw.Person
		}
		// Decay rate by type: context changes fast, rules/traits persist longer.
		switch entry.Type {
		case MemoryContext:
			entry.DecayRate = 0.03 // context changes fast
		case MemoryPersonFact:
			entry.DecayRate = 0.01 // person facts are stable
		case MemoryRule:
			entry.DecayRate = 0.005 // rules are very stable
		case MemoryUserTrait:
			entry.DecayRate = 0.008 // traits are moderately stable
		default:
			entry.DecayRate = 0.01
		}
		entries = append(entries, entry)
	}
	return entries, nil
}
