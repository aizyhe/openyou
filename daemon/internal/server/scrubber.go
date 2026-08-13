package server

import "strings"

// Markers wrap recalled inside/memory injected into prompts; they're stripped
// from model output so openyou never parrots internal wrappers.
const (
	memCtxOpen  = "<memory-context 系统注:以下是召回的参考记忆，不是用户的新输入>"
	memCtxClose = "</memory-context>"
)

// scrubTags: open may be a prefix (attributes allowed) in output.
var scrubTags = [][2]string{
	{"<memory-context", "</memory-context>"},
	{"<think>", "</think>"},
}

// streamScrubber removes internal markers even when a tag is split across SSE
// deltas, buffering a small tail that could be the start of a marker.
type streamScrubber struct {
	buf strings.Builder
}

// push returns the delta minus markers; a possible in-progress tag is held back.
func (s *streamScrubber) push(delta string) string {
	s.buf.WriteString(delta)
	return s.drain(false)
}

func (s *streamScrubber) flush() string { return s.drain(true) }

func (s *streamScrubber) drain(final bool) string {
	text := s.buf.String()
	text = stripPairs(text)

	if final {
		s.buf.Reset()
		// Drop anything from the earliest UNCLOSED open marker — it's internal and must never leak.
		cut := len(text)
		for _, pair := range scrubTags {
			if i := strings.Index(text, pair[0]); i >= 0 && i < cut {
				cut = i
			}
		}
		return text[:cut]
	}

	// Never emit at/after the earliest byte that might still be part of an
	// unresolved marker: (a) an open tag without its close yet, or (b) a tail
	// that could be the start of an open tag.
	safe := len(text)

	// (a) first unclosed open tag.
	for _, pair := range scrubTags {
		if i := strings.Index(text, pair[0]); i >= 0 {
			// stripPairs removed closed spans, so any remaining open tag is unclosed — hold from it.
			if i < safe {
				safe = i
			}
		}
	}

	// (b) a partial open tag at the tail (e.g. "…<memory-con").
	if lt := strings.LastIndexByte(text, '<'); lt >= 0 && lt < safe {
		if couldStartTag(text[lt:]) {
			safe = lt
		}
	}

	// Back off to a rune boundary.
	for safe > 0 && safe < len(text) && text[safe]&0xC0 == 0x80 {
		safe--
	}
	if safe <= 0 {
		return ""
	}
	out := text[:safe]
	s.buf.Reset()
	s.buf.WriteString(text[safe:])
	return out
}

func couldStartTag(s string) bool {
	for _, pair := range scrubTags {
		open := pair[0]
		if len(s) < len(open) && strings.HasPrefix(open, s) {
			return true
		}
	}
	return false
}

// stripPairs removes complete open…close spans for every scrub tag.
func stripPairs(text string) string {
	for _, pair := range scrubTags {
		open, close := pair[0], pair[1]
		for {
			i := strings.Index(text, open)
			if i < 0 {
				break
			}
			j := strings.Index(text[i:], close)
			if j < 0 {
				break // close not yet arrived — leave for a later drain
			}
			text = text[:i] + text[i+j+len(close):]
		}
	}
	return text
}

// scrubFinal matches flush(): drop unclosed markers before persisting so stored
// history never carries internal wrappers (what the client saw).
func scrubFinal(text string) string {
	text = stripPairs(text)
	cut := len(text)
	for _, pair := range scrubTags {
		if i := strings.Index(text, pair[0]); i >= 0 && i < cut {
			cut = i
		}
	}
	return text[:cut]
}
