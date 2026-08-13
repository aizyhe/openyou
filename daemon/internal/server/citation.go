package server

import (
	"regexp"
	"strings"
)

// Citation: `From` matches the frontend's cite-<from> class — person | rule | skill | external | unverified.
type Citation struct {
	From  string `json:"from"`
	Title string `json:"title"`
}

// emitCitations sends parsed markers, or for a substantive reply with none a
// single "unverified" citation so the UI can flag ungrounded answers.
func emitCitations(sse *sseWriter, text string) {
	if cites := parseCitations(text); len(cites) > 0 {
		sse.send(map[string]any{"type": "source-citations", "citations": cites})
		return
	}
	if len([]rune(strings.TrimSpace(text))) >= 40 {
		sse.send(map[string]any{"type": "source-citations", "citations": []Citation{
			{From: "unverified", Title: "此回答未标注依据来源"},
		}})
	}
}

// citationRe matches the markers the prompts instruct the model to emit:
// [👤person] [📋rule#2] [🧠skill] [🌐external]. Group 1 = emoji tag, group 2 = label.
var citationRe = regexp.MustCompile(`\[(👤|📋|🧠|🌐)([^\]]*)\]`)

var citationKind = map[string]string{
	"👤": "person",
	"📋": "rule",
	"🧠": "skill",
	"🌐": "external",
}

func parseCitations(text string) []Citation {
	matches := citationRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var out []Citation
	for _, m := range matches {
		from := citationKind[m[1]]
		title := strings.TrimSpace(m[2])
		if from == "" {
			continue
		}
		key := from + "|" + title
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Citation{From: from, Title: title})
	}
	return out
}
