package memory

import "testing"

// staticExtractor returns the same entries every call, simulating repeated
// extraction over overlapping conversation windows.
type staticExtractor struct{ entries []MemoryEntry }

func (e *staticExtractor) Extract(_ string) ([]MemoryEntry, error) { return e.entries, nil }

func TestSaveDedupedSkipsDuplicates(t *testing.T) {
	store := newTestStore(t)
	q := &Queue{store: store}

	e := MemoryEntry{
		Type:       MemoryPersonFact,
		Person:     "张伟",
		Content:    "张伟最近开始关注成本控制",
		Confidence: 0.9,
	}
	// Save the same fact three times (as repeated extraction would).
	q.saveDeduped(e)
	q.saveDeduped(e)
	q.saveDeduped(e)

	all, err := store.ListAll()
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("duplicate memories should be deduped to 1, got %d", len(all))
	}
}

func TestSaveDedupedKeepsDistinct(t *testing.T) {
	store := newTestStore(t)
	q := &Queue{store: store}

	q.saveDeduped(MemoryEntry{Type: MemoryPersonFact, Person: "张伟", Content: "张伟喜欢用数据说话", Confidence: 0.9})
	q.saveDeduped(MemoryEntry{Type: MemoryPersonFact, Person: "李娜", Content: "李娜负责市场部预算审批", Confidence: 0.9})
	q.saveDeduped(MemoryEntry{Type: MemoryUserTrait, Content: "在不确定时先收集信息再决定", Confidence: 0.9})

	all, _ := store.ListAll()
	if len(all) != 3 {
		t.Errorf("distinct memories should all be kept, got %d", len(all))
	}
}

// TestSaveDedupedKeepsCorrectedChineseFact guards the CJK data-loss bug: a NEW
// corrected fact that is a substring of an OLD contradicting one must NOT be
// dropped as a duplicate (opposing polarity now works for Chinese).
func TestSaveDedupedKeepsCorrectedChineseFact(t *testing.T) {
	store := newTestStore(t)
	q := &Queue{store: store}

	// Old fact, then the user corrects it — opposite polarity, and the new
	// content ("喜欢喝酒") is a substring of the old ("不喜欢喝酒").
	q.saveDeduped(MemoryEntry{Type: MemoryPersonFact, Person: "王五", Content: "王五不喜欢喝酒", Confidence: 0.9})
	q.saveDeduped(MemoryEntry{Type: MemoryPersonFact, Person: "王五", Content: "王五喜欢喝酒", Confidence: 0.9})

	all, _ := store.ListAll()
	if len(all) != 2 {
		t.Errorf("a corrected (opposite-polarity) Chinese fact must be kept, got %d memories", len(all))
	}
}

// TestSaveDedupedChineseTrueDuplicate: a genuinely-duplicate Chinese fact (same
// topic, same polarity) IS deduped via the CJK bigram overlap pass.
func TestSaveDedupedChineseTrueDuplicate(t *testing.T) {
	store := newTestStore(t)
	q := &Queue{store: store}

	q.saveDeduped(MemoryEntry{Type: MemoryPersonFact, Person: "赵六", Content: "赵六最近很关注成本控制这件事", Confidence: 0.9})
	// Reworded but same topic + same (non-opposing) polarity → should dedup.
	q.saveDeduped(MemoryEntry{Type: MemoryPersonFact, Person: "赵六", Content: "赵六关注成本控制", Confidence: 0.9})

	all, _ := store.ListAll()
	if len(all) != 1 {
		t.Errorf("same-topic Chinese duplicate should dedup to 1, got %d", len(all))
	}
}

func TestOpposingPolarityChinese(t *testing.T) {
	if !opposingPolarity("喜欢喝酒", "不喜欢喝酒") {
		t.Error("like vs dislike should be opposing polarity")
	}
	if opposingPolarity("喜欢喝酒", "喜欢喝茶") {
		t.Error("two positives should NOT be opposing")
	}
	if !opposingPolarity("支持这个方案", "反对这个方案") {
		t.Error("support vs oppose should be opposing")
	}
	// A negated negative word reads as positive/neutral, so it must NOT be
	// treated as opposing a positive statement (regression: negation-flip was
	// only applied to positive words, not negative ones).
	if opposingPolarity("不讨厌喝酒", "喜欢喝酒") {
		t.Error("negated negative (don't dislike) should NOT oppose like")
	}
	if !opposingPolarity("讨厌喝酒", "喜欢喝酒") {
		t.Error("dislike vs like should be opposing")
	}
}
