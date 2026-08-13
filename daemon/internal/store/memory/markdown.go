package memory

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MarkdownDir is the on-disk home for memory journals, set once at startup; when
// empty, markdown persistence is disabled.
var MarkdownDir string

// MemoMarkdown is one month's document ("2026-08.md") — a browsable journal, not
// one ever-growing blob.
type MemoMarkdown struct {
	Month   string
	Entries []MemoryEntry
}

// monthOf returns the "YYYY-MM" key in LOCAL time, so month boundaries match what
// the user sees (UTC attribution would misfile between local 00:00-08:00 at UTC+8).
func monthOf(ts int64) string {
	return time.Unix(0, ts).Local().Format("2006-01")
}

// writeMarkdown rebuilds a month's journal from ALL its stored memories, so user
// edits show up and deleted entries disappear. Idempotent — safe per save.
func (s *Store) writeMarkdown(e MemoryEntry) {
	if MarkdownDir == "" {
		return
	}
	s.markdownMu.Lock()
	defer s.markdownMu.Unlock()
	month := monthOf(e.CreatedAt)
	path := filepath.Join(MarkdownDir, month+".md")

	entries, err := s.listByMonth(month)
	if err != nil {
		log.Printf("memory: markdown %s: list: %v", month, err)
		return
	}
	if len(entries) == 0 {
		_ = os.Remove(path) // month fully emptied
		return
	}
	body := renderMarkdown(month, entries)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0644); err != nil {
		log.Printf("memory: markdown %s: write: %v", month, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("memory: markdown %s: rename: %v", month, err)
		return
	}
}

func (s *Store) listByMonth(month string) ([]MemoryEntry, error) {
	start, err := time.ParseInLocation("2006-01", month, time.Local)
	if err != nil {
		return nil, fmt.Errorf("parse month %q: %w", month, err)
	}
	end := start.AddDate(0, 1, 0)
	rows, err := s.db.Query(
		`SELECT id, type, person, content, confidence, source_session,
		 confirmed, suggested, disputed, decay_rate, relevance_score, tags, created_at, updated_at
		 FROM context_memory
		 WHERE created_at >= ? AND created_at < ?
		 ORDER BY created_at ASC`,
		start.UnixNano(), end.UnixNano(),
	)
	if err != nil {
		return nil, fmt.Errorf("list by month: %w", err)
	}
	defer rows.Close()
	return scanMemories(rows)
}

func renderMarkdown(month string, entries []MemoryEntry) string {
	var b strings.Builder
	b.WriteString("# 记忆 — " + month + "\n\n")
	b.WriteString("> AI 从对话中自主提取的记忆。你可以直接编辑本文件或 App 内 Memories 页；\n")
	b.WriteString("> 编辑以 SQLite 为准会同步覆盖此处（本文件是运行时生成的可读视图）。\n\n")

	typeGroups := []struct {
		key, title string
	}{
		{"person_fact", "人物新信息"},
		{"user_trait", "你的偏好"},
		{"rule", "行为规律"},
		{"context", "当前情境"},
	}
	for _, g := range typeGroups {
		var items []MemoryEntry
		for _, e := range entries {
			if string(e.Type) == g.key {
				items = append(items, e)
			}
		}
		if len(items) == 0 {
			continue
		}
		b.WriteString("## " + g.title + "\n\n")
		for _, e := range items {
			ts := time.Unix(0, e.CreatedAt).Local().Format("2006-01-02 15:04")
			state := "✅ 已确认"
			if !e.Confirmed {
				state = "🟡 待确认"
			}
			person := ""
			if e.Person != "" {
				person = " · " + e.Person
			}
			b.WriteString(fmt.Sprintf("- **%s** %s%s\n", state, ts, person))
			b.WriteString("  " + e.Content + "\n")
			b.WriteString("  > 备注: \n\n")
		}
	}

	// Any type not in the fixed groups (e.g. future types) rendered flat.
	var rest []MemoryEntry
	for _, e := range entries {
		known := false
		for _, g := range typeGroups {
			if string(e.Type) == g.key {
				known = true
				break
			}
		}
		if !known {
			rest = append(rest, e)
		}
	}
	if len(rest) > 0 {
		sort.SliceStable(rest, func(i, j int) bool { return rest[i].CreatedAt < rest[j].CreatedAt })
		b.WriteString("## 其他\n\n")
		for _, e := range rest {
			ts := time.Unix(0, e.CreatedAt).Local().Format("2006-01-02 15:04")
			b.WriteString(fmt.Sprintf("- [%s] %s: %s\n", ts, e.Type, e.Content))
		}
	}
	return b.String()
}
