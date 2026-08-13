package matcher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Scenario 1: manual UI edits → RefreshDoc takes effect immediately (O(1))
func TestRefreshDoc_Immediate(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "inside"), 0755)
	os.WriteFile(filepath.Join(dir, "inside", "salary.md"),
		[]byte("# 薪资谈判记录\n\n- 期望 70K\n"), 0644)
	m, err := NewMatcherWithDir(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Modify content → RefreshDoc
	os.WriteFile(filepath.Join(dir, "inside", "salary.md"),
		[]byte("# 薪资谈判记录\n\n- 期望 85K 保底\n"), 0644)
	if err := m.RefreshDoc("inside/salary.md"); err != nil {
		t.Fatal(err)
	}
	// The next retrieval immediately hits the new content
	docs := m.RelevantDocs("85K 保底", 5000)
	found := false
	for _, d := range docs {
		if strings.Contains(d, "85K") {
			found = true
		}
	}
	fmt.Println("UI edit took effect immediately:", found)
	if !found {
		t.Error("new content not effective after RefreshDoc")
	}
	// Delete → RefreshDoc → gone
	os.Remove(filepath.Join(dir, "inside", "salary.md"))
	if err := m.RefreshDoc("inside/salary.md"); err != nil {
		t.Fatal(err)
	}
	docs = m.RelevantDocs("薪资", 5000)
	fmt.Println("retrieval result count after UI delete:", len(docs))
	if len(docs) != 0 {
		t.Error("deleted doc still retrievable after delete")
	}
}

// Scenario 2: editing files directly (bypassing the API) → AI entry points auto-detect
func TestRefreshIfChanged_DirectEdit(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "inside"), 0755)
	os.WriteFile(filepath.Join(dir, "inside", "plan.md"),
		[]byte("# 旧计划\n\n- 旧目标\n"), 0644)
	m, err := NewMatcherWithDir(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Initialize the scan baseline once
	_ = m.ListDocs()
	time.Sleep(1100 * time.Millisecond) // wait past the refresh time window

	// Edit the file directly, calling no refresh method
	os.WriteFile(filepath.Join(dir, "inside", "plan.md"),
		[]byte("# 新计划\n\n- 冲刺 100K 期权\n"), 0644)
	// Add a new file
	os.WriteFile(filepath.Join(dir, "inside", "new-note.md"),
		[]byte("# 新笔记\n\n- 昇腾 CANN 适配\n"), 0644)

	// Call the AI entry point → it should auto-detect
	docs := m.RelevantDocs("昇腾 CANN", 5000)
	found := false
	for _, d := range docs {
		if strings.Contains(d, "昇腾") {
			found = true
		}
	}
	fmt.Println("AI entry auto-detected new doc after direct file edit:", found)
	if !found {
		t.Error("refreshIfChanged did not detect the new file")
	}

	// Delete a file directly → the entry point auto-detects
	os.Remove(filepath.Join(dir, "inside", "plan.md"))
	time.Sleep(1100 * time.Millisecond)
	docs = m.RelevantDocs("100K 期权", 5000)
	fmt.Println("retrieval result count after direct file delete:", len(docs))
	if len(docs) != 0 {
		t.Error("direct delete not detected")
	}
}

// Scenario 3: performance with tens of thousands of docs — RefreshDoc is O(1),
// independent of the doc count
func TestRefreshDoc_Perf(t *testing.T) {
	dir := t.TempDir()
	// Create 2000 docs
	for i := 0; i < 2000; i++ {
		p := filepath.Join(dir, "inside", "bulk", fmt.Sprintf("doc-%04d.md", i))
		os.MkdirAll(filepath.Dir(p), 0755)
		os.WriteFile(p, []byte(fmt.Sprintf("# 文档 %04d\n\n- 填充内容 %04d\n", i, i)), 0644)
	}
	start := time.Now()
	m, err := NewMatcherWithDir(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("initial load of 2000 docs: %v\n", time.Since(start))

	// Edit 1 file → RefreshDoc
	one := filepath.Join(dir, "inside", "bulk", "doc-0001.md")
	os.WriteFile(one, []byte("# 文档 0001 已改\n\n- 新增关键词 RAREKEY\n"), 0644)
	start = time.Now()
	if err := m.RefreshDoc("inside/bulk/doc-0001.md"); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	fmt.Printf("single RefreshDoc with 2000 docs: %v\n", elapsed)
	docs := m.RelevantDocs("RAREKEY", 5000)
	if len(docs) == 0 {
		t.Error("new keyword not retrievable after refresh")
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("RefreshDoc too slow: %v", elapsed)
	}
}
