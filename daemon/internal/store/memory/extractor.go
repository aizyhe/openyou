package memory

import (
	"strings"
)

type MemoryExtractor interface {
	Extract(conversationText string) ([]MemoryEntry, error)
}

type MockExtractor struct {
	Entries []MemoryEntry
}

func (m *MockExtractor) Extract(_ string) ([]MemoryEntry, error) {
	return m.Entries, nil
}

// Per-type auto-save confidence thresholds: at/above → auto-save; between the
// discard floor and threshold → flag for user confirmation.
const (
	thresholdPersonFact = 0.80
	thresholdUserTrait  = 0.85
	thresholdRule       = 0.75
	thresholdContext    = 0.80
	// thresholdDefault: unknown types fall back to the strictest common bar.
	thresholdDefault = 0.85

	// discardFloor: below this, entries are discarded regardless of type.
	discardFloor = 0.60
)

func autoSaveThreshold(t MemoryType) float64 {
	switch t {
	case MemoryPersonFact:
		return thresholdPersonFact
	case MemoryUserTrait:
		return thresholdUserTrait
	case MemoryRule:
		return thresholdRule
	case MemoryContext:
		return thresholdContext
	default:
		return thresholdDefault
	}
}

func ClassifyConfidence(entries []MemoryEntry) ConfidenceDecision {
	decision := ConfidenceDecision{}

	for _, e := range entries {
		threshold := autoSaveThreshold(e.Type)
		switch {
		case e.Confidence >= threshold:
			e.Suggested = false
			decision.HighConfidence = append(decision.HighConfidence, e)
		case e.Confidence >= discardFloor:
			e.Suggested = true
			decision.MediumConfidence = append(decision.MediumConfidence, e)
		default:
			e.Suggested = false
			decision.LowConfidence = append(decision.LowConfidence, e)
		}
	}

	return decision
}

// FindConflicts detects genuine contradictions: same person+type, a shared topic
// word, AND opposing sentiment polarity — polarity-less conflicts are left to LLM reconciliation.
func FindConflicts(newEntries, existingMemories []MemoryEntry) []Conflict {
	var conflicts []Conflict

	for _, newEntry := range newEntries {
		if newEntry.Person == "" {
			continue
		}
		for _, existing := range existingMemories {
			if existing.Person == "" || existing.Person != newEntry.Person {
				continue
			}
			if existing.Type != newEntry.Type {
				continue
			}
			if sharedWordsCount(newEntry.Content, existing.Content, newEntry.Person) >= 1 &&
				opposingPolarity(newEntry.Content, existing.Content) {
				conflicts = append(conflicts, Conflict{
					New:      newEntry,
					Existing: existing,
					Reason:   "contradictory facts about " + newEntry.Person,
				})
			}
		}
	}

	return conflicts
}

// Sentiment-polarity markers distinguishing a real contradiction from topic overlap.
var positiveWords = map[string]bool{
	"love": true, "loves": true, "loved": true, "like": true, "likes": true,
	"liked": true, "enjoy": true, "enjoys": true, "enjoyed": true,
	"prefer": true, "prefers": true, "preferred": true, "want": true,
	"wants": true, "wanted": true, "happy": true, "good": true, "great": true,
	"favor": true, "favors": true, "supports": true, "support": true,
}

var negativeWords = map[string]bool{
	"hate": true, "hates": true, "hated": true, "dislike": true,
	"dislikes": true, "disliked": true, "avoid": true, "avoids": true,
	"avoided": true, "unhappy": true, "bad": true,
	"never": true, "oppose": true, "opposes": true, "opposed": true,
	"refuse": true, "refuses": true, "refused": true,
}

// opposingPolarity reports whether one string is positive and the other negative.
func opposingPolarity(a, b string) bool {
	posA, negA := polarity(a)
	posB, negB := polarity(b)
	return (posA && negB) || (negA && posB)
}

// polarity classifies sentiment markers: English words + Chinese substrings (CJK
// has no spaces); a preceding negation marker (不/没/无/非/别/勿) flips polarity.
func polarity(s string) (positive, negative bool) {
	for _, f := range strings.Fields(strings.ToLower(s)) {
		f = strings.TrimRight(f, ".,;:!?\"'()[]{}")
		if positiveWords[f] {
			positive = true
		}
		if negativeWords[f] {
			negative = true
		}
	}
	for _, w := range zhPositive {
		for _, idx := range allIndex(s, w) {
			if precededByNegation(s, idx) {
				negative = true
			} else {
				positive = true
			}
		}
	}
	for _, w := range zhNegative {
		for _, idx := range allIndex(s, w) {
			// A negated negative word ("不讨厌" = not dislike) reads positive, so flip
			// it too — otherwise it would spuriously contradict "喜欢喝酒" (likes drinking).
			if precededByNegation(s, idx) {
				positive = true
			} else {
				negative = true
			}
		}
	}
	return positive, negative
}

var zhPositive = []string{"喜欢", "喜爱", "赞成", "支持", "认可", "满意", "愿意", "偏好", "擅长", "信任"}
var zhNegative = []string{"讨厌", "反对", "拒绝", "回避", "不满", "厌恶", "抵触", "怀疑"}

var zhNegations = []string{"不", "没", "无", "非", "别", "勿", "未"}

func precededByNegation(s string, i int) bool {
	if i == 0 {
		return false
	}
	prefix := s[:i]
	for _, neg := range zhNegations {
		if strings.HasSuffix(prefix, neg) {
			return true
		}
	}
	return false
}

func allIndex(s, sub string) []int {
	var out []int
	for off := 0; ; {
		i := strings.Index(s[off:], sub)
		if i < 0 {
			break
		}
		out = append(out, off+i)
		off += i + len(sub)
	}
	return out
}

// sharedWordsCount counts significant shared tokens (excluding the person name);
// for Chinese it falls back to shared CJK 2-grams so same-topic facts are detected.
func sharedWordsCount(a, b, person string) int {
	wordsA := extractSignificantWords(a, person)
	wordsB := extractSignificantWords(b, person)

	set := make(map[string]bool, len(wordsA))
	for _, w := range wordsA {
		set[w] = true
	}

	count := 0
	for _, w := range wordsB {
		if set[w] {
			count++
		}
	}

	if count < 2 {
		count += sharedCJKBigrams(a, b, person)
	}
	return count
}

func sharedCJKBigrams(a, b, person string) int {
	ban := cjkBigrams(person)
	ga, gb := cjkBigrams(a), cjkBigrams(b)
	n := 0
	for g := range ga {
		if gb[g] && !ban[g] {
			n++
		}
	}
	return n
}

func cjkBigrams(s string) map[string]bool {
	out := map[string]bool{}
	runes := []rune(s)
	for i := 0; i+1 < len(runes); i++ {
		if isHan(runes[i]) && isHan(runes[i+1]) {
			out[string(runes[i:i+2])] = true
		}
	}
	return out
}

func isHan(r rune) bool { return r >= 0x4E00 && r <= 0x9FFF }

var stopwords = map[string]bool{
	"a": true, "an": true, "the": true, "is": true, "are": true, "was": true,
	"were": true, "has": true, "have": true, "had": true, "been": true,
	"in": true, "on": true, "at": true, "to": true, "for": true, "of": true,
	"with": true, "from": true, "by": true, "as": true, "that": true,
	"this": true, "it": true, "its": true, "and": true, "or": true, "but": true,
	"not": true, "no": true, "so": true, "just": true, "about": true,
	"recently": true, "now": true, "still": true, "always": true,
	"very": true, "really": true, "quite": true, "too": true,
	"she": true, "he": true, "they": true, "who": true, "which": true,
	"her": true, "his": true, "their": true,
}

func extractSignificantWords(text, person string) []string {
	fields := strings.Fields(strings.ToLower(text))
	words := make([]string, 0, len(fields))
	personLower := strings.ToLower(person)

	for _, f := range fields {
		f = strings.TrimRight(f, ".,;:!?\"'()[]{}")
		if len(f) < 3 {
			continue
		}
		if stopwords[f] {
			continue
		}
		if f == personLower {
			continue
		}
		words = append(words, f)
	}
	return words
}
