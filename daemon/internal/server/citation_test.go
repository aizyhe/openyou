package server

import "testing"

func TestParseCitations(t *testing.T) {
	text := "私聊降级 [👤张建国档案]，用规则包装 [📋规则#2]，参考 [🧠权力分析技能]。" +
		"外部数据见 [🌐外部搜索]。重复的 [📋规则#2] 应去重。"
	got := parseCitations(text)
	if len(got) != 4 {
		t.Fatalf("want 4 unique citations, got %d: %+v", len(got), got)
	}
	want := map[string]string{
		"person":   "张建国档案",
		"rule":     "规则#2",
		"skill":    "权力分析技能",
		"external": "外部搜索",
	}
	for _, c := range got {
		if want[c.From] != c.Title {
			t.Errorf("from=%s: want title %q, got %q", c.From, want[c.From], c.Title)
		}
	}
}

func TestParseCitationsNone(t *testing.T) {
	if got := parseCitations("没有任何标记的普通回复。"); got != nil {
		t.Errorf("want nil, got %+v", got)
	}
}
