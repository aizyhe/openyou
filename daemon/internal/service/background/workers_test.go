package background

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"openyou/daemon/internal/store/memory"
	"openyou/daemon/internal/store/task"

	_ "github.com/mattn/go-sqlite3"
)

// ---------------------------------------------------------------------------
// mockHub — records Broadcast calls for assertion
// ---------------------------------------------------------------------------

type mockHub struct {
	mu   sync.Mutex
	msgs [][]byte
}

func (h *mockHub) Broadcast(msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cp := make([]byte, len(msg))
	copy(cp, msg)
	h.msgs = append(h.msgs, cp)
}

func (h *mockHub) messages() [][]byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([][]byte, len(h.msgs))
	copy(out, h.msgs)
	return out
}

func (h *mockHub) messageCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.msgs)
}

func (h *mockHub) waitForMessages(t *testing.T, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if h.messageCount() >= n {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %d messages, got %d", n, h.messageCount())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// ---------------------------------------------------------------------------
// test helpers
// ---------------------------------------------------------------------------

func parseBroadcast(msgs [][]byte) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(msgs))
	for _, m := range msgs {
		var v map[string]interface{}
		if err := json.Unmarshal(m, &v); err == nil {
			out = append(out, v)
		}
	}
	return out
}

func broadcastType(msgs [][]byte) string {
	parsed := parseBroadcast(msgs)
	if len(parsed) == 0 {
		return ""
	}
	t, _ := parsed[0]["type"].(string)
	return t
}

func newTaskStore(t *testing.T) *task.Store {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// Pin to one connection: every extra pool connection to :memory: is a
	// fresh empty database, so under load a second connection intermittently
	// sees "no such table" (the schema lives on the first connection only).
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	ts, err := task.NewStore(db)
	if err != nil {
		t.Fatalf("task.NewStore: %v", err)
	}
	return ts
}

func newMemStore(t *testing.T) *memory.Store {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// Pin to one connection: every extra pool connection to :memory: is a
	// fresh empty database, so under load a second connection intermittently
	// sees "no such table" (the schema lives on the first connection only).
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	ms, err := memory.NewStore(db)
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	return ms
}

// ---------------------------------------------------------------------------
// 1. checkStar — detects pending tasks
// ---------------------------------------------------------------------------

func TestWorkers_CheckStar_PendingTasks(t *testing.T) {
	hub := &mockHub{}
	ts := newTaskStore(t)
	ts.Create("Review Q3 report", "", "", "user", "", time.Now().Unix())
	ts.Create("Schedule 1:1 with Alice", "", "", "user", "", time.Now().Unix())
	w := &Workers{tasks: ts, hub: hub, lastDecay: make(map[string]int64)}

	w.checkStar()

	msgs := hub.messages()
	if n := len(msgs); n != 1 {
		t.Fatalf("expected 1 broadcast, got %d", n)
	}

	parsed := parseBroadcast(msgs)
	if len(parsed) != 1 {
		t.Fatal("broadcast message is not valid JSON")
	}

	got := parsed[0]
	if typ, _ := got["type"].(string); typ != "star-check" {
		t.Errorf("expected type=star-check, got %q", typ)
	}
	if pending, ok := got["pending"].(float64); !ok || pending != 2 {
		t.Errorf("expected pending=2, got %v", got["pending"])
	}
	if _, ok := got["time"]; !ok {
		t.Error("broadcast missing 'time' field")
	}
}

// ---------------------------------------------------------------------------
// 2. checkStar — skips when no pending tasks
// ---------------------------------------------------------------------------

func TestWorkers_CheckStar_NoPendingTasks(t *testing.T) {
	hub := &mockHub{}
	ts := newTaskStore(t)
	w := &Workers{tasks: ts, hub: hub, lastDecay: make(map[string]int64)}

	w.checkStar()

	if n := hub.messageCount(); n != 0 {
		t.Errorf("expected 0 broadcasts when no tasks, got %d", n)
	}
}

// ---------------------------------------------------------------------------
// 3. checkStar — deduplicates (same hash = no broadcast on second call)
// ---------------------------------------------------------------------------

func TestWorkers_CheckStar_Deduplicate(t *testing.T) {
	hub := &mockHub{}
	ts := newTaskStore(t)
	ts.Create("Important task", "", "", "user", "", time.Now().Unix())
	w := &Workers{tasks: ts, hub: hub, lastDecay: make(map[string]int64)}

	w.checkStar()
	w.checkStar()

	if n := hub.messageCount(); n != 1 {
		t.Errorf("expected 1 broadcast (dedup), got %d", n)
	}
}

func TestWorkers_CheckStar_RebroadcastAfterChange(t *testing.T) {
	hub := &mockHub{}
	ts := newTaskStore(t)
	ts.Create("Task 1", "", "", "user", "", time.Now().Unix())
	w := &Workers{tasks: ts, hub: hub, lastDecay: make(map[string]int64)}
	w.checkStar()

	ts.Create("Task 2", "", "", "user", "", time.Now().Unix())
	w.checkStar()

	if n := hub.messageCount(); n != 2 {
		t.Errorf("expected 2 broadcasts after content change, got %d", n)
	}
}

// ---------------------------------------------------------------------------
// 4. countPastDueTasks
// ---------------------------------------------------------------------------

func TestWorkers_CountPastDueTasks_PastDates(t *testing.T) {
	ts := newTaskStore(t)
	ts.Create("get feedback", "", "2020-01-15", "user", "", time.Now().Unix())
	ts.Create("send memo", "", "2019-06-01", "user", "", time.Now().Unix())
	ts.Create("future plan", "", "2099-12-31", "user", "", time.Now().Unix())
	w := &Workers{tasks: ts, lastDecay: make(map[string]int64)}

	count := w.countPastDueTasks()
	if count != 2 {
		t.Errorf("expected 2 past-due tasks, got %d", count)
	}
}

func TestWorkers_CountPastDueTasks_NoDates(t *testing.T) {
	ts := newTaskStore(t)
	ts.Create("Task without date", "", "", "user", "", time.Now().Unix())
	w := &Workers{tasks: ts, lastDecay: make(map[string]int64)}

	count := w.countPastDueTasks()
	if count != 0 {
		t.Errorf("expected 0 when no dates present, got %d", count)
	}
}

func TestWorkers_CountPastDueTasks_EmptyTasks(t *testing.T) {
	ts := newTaskStore(t)
	w := &Workers{tasks: ts, lastDecay: make(map[string]int64)}

	count := w.countPastDueTasks()
	if count != 0 {
		t.Errorf("expected 0 with empty tasks, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// 5. countRuleConflicts
// ---------------------------------------------------------------------------

func TestWorkers_CountRuleConflicts_ConflictFound(t *testing.T) {
	w := &Workers{rules: &mockRules{rules: "When criticized, confront directly. But sometimes it's better to avoid confrontation entirely."}, lastDecay: make(map[string]int64)}

	count := w.countRuleConflicts()
	if count != 1 {
		t.Errorf("expected 1 conflict, got %d", count)
	}
}

func TestWorkers_CountRuleConflicts_NoConflict(t *testing.T) {
	w := &Workers{rules: &mockRules{rules: "Always speak up in meetings when you have an idea."}, lastDecay: make(map[string]int64)}

	count := w.countRuleConflicts()
	if count != 0 {
		t.Errorf("expected 0 conflicts, got %d", count)
	}
}

func TestWorkers_CountRuleConflicts_EmptyRules(t *testing.T) {
	w := &Workers{rules: &mockRules{}, lastDecay: make(map[string]int64)}

	count := w.countRuleConflicts()
	if count != 0 {
		t.Errorf("expected 0 with no rules, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// checkInsideHealth
// ---------------------------------------------------------------------------

func TestWorkers_CheckInsideHealth_PastDueTasks(t *testing.T) {
	hub := &mockHub{}
	ts := newTaskStore(t)
	ts.Create("reporting", "", "2020-03-15", "user", "", time.Now().Unix())
	w := &Workers{tasks: ts, hub: hub, rules: &mockRules{}, lastDecay: make(map[string]int64)}

	w.checkInsideHealth()

	msgs := hub.messages()
	if n := len(msgs); n != 1 {
		t.Fatalf("expected 1 broadcast, got %d", n)
	}

	parsed := parseBroadcast(msgs)
	if typ, _ := parsed[0]["type"].(string); typ != "context-health" {
		t.Errorf("expected type=context-health, got %q", typ)
	}
	if pastDue, ok := parsed[0]["past_due_tasks"].(float64); !ok || pastDue != 1 {
		t.Errorf("expected past_due_tasks=1, got %v", parsed[0]["past_due_tasks"])
	}
	if ruleConflicts, ok := parsed[0]["rule_conflicts"].(float64); !ok || ruleConflicts != 0 {
		t.Errorf("expected rule_conflicts=0, got %v", parsed[0]["rule_conflicts"])
	}
}

func TestWorkers_CheckInsideHealth_RuleConflicts(t *testing.T) {
	hub := &mockHub{}
	w := &Workers{hub: hub, rules: &mockRules{rules: "speak up in meetings. But sometimes it's better to stay silent."}, lastDecay: make(map[string]int64)}

	w.checkInsideHealth()

	msgs := hub.messages()
	if n := len(msgs); n != 1 {
		t.Fatalf("expected 1 broadcast, got %d", n)
	}
	parsed := parseBroadcast(msgs)
	if conflicts, ok := parsed[0]["rule_conflicts"].(float64); !ok || conflicts != 1 {
		t.Errorf("expected rule_conflicts=1, got %v", parsed[0]["rule_conflicts"])
	}
}

func TestWorkers_CheckInsideHealth_NoIssues(t *testing.T) {
	hub := &mockHub{}
	ts := newTaskStore(t)
	ts.Create("some task", "", "2099-01-01", "user", "", time.Now().Unix())
	w := &Workers{tasks: ts, hub: hub, rules: &mockRules{rules: "Always be respectful."}, lastDecay: make(map[string]int64)}

	w.checkInsideHealth()

	if n := hub.messageCount(); n != 0 {
		t.Errorf("expected 0 broadcasts when no issues, got %d", n)
	}
}

func TestWorkers_CheckInsideHealth_Deduplicate(t *testing.T) {
	hub := &mockHub{}
	w := &Workers{hub: hub, rules: &mockRules{rules: "confront directly, but also avoid confrontation"}, lastDecay: make(map[string]int64)}

	w.checkInsideHealth()
	w.checkInsideHealth()

	if n := hub.messageCount(); n != 1 {
		t.Errorf("expected 1 broadcast (dedup), got %d", n)
	}
}

// ---------------------------------------------------------------------------
// BUG 2 (adapted) — countPastDueTasks: only incomplete tasks with past end_date
// ---------------------------------------------------------------------------

func TestWorkers_CountPastDueTasks_OnlyPastDates(t *testing.T) {
	ts := newTaskStore(t)
	ts.Create("past due task", "", "2020-01-15", "user", "", time.Now().Unix())
	ts.Create("no date task", "", "", "user", "", time.Now().Unix())
	ts.Create("future task", "", "2099-12-31", "user", "", time.Now().Unix())
	w := &Workers{tasks: ts, lastDecay: make(map[string]int64)}

	count := w.countPastDueTasks()
	if count != 1 {
		t.Errorf("expected 1 past-due task, got %d", count)
	}
}

func TestWorkers_CountPastDueTasks_NoDatesPresent(t *testing.T) {
	ts := newTaskStore(t)
	ts.Create("没有日期的待办", "", "", "user", "", time.Now().Unix())
	ts.Create("另一个待办", "", "", "user", "", time.Now().Unix())
	w := &Workers{tasks: ts, lastDecay: make(map[string]int64)}

	if count := w.countPastDueTasks(); count != 0 {
		t.Errorf("expected 0 past-due, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// BUG 3 — countRuleConflicts: detects a Chinese contradictory pair
// ---------------------------------------------------------------------------

func TestWorkers_CountRuleConflicts_ChinesePair(t *testing.T) {
	w := &Workers{rules: &mockRules{rules: "#1. 遇到分歧时应当正面冲突,把话说清楚。\n" +
		"#2. 在职场里最好避免冲突,保存关系资本。"}, lastDecay: make(map[string]int64)}

	if count := w.countRuleConflicts(); count != 1 {
		t.Errorf("expected 1 conflict for Chinese pair, got %d", count)
	}
}

func TestWorkers_CountRuleConflicts_ChineseNoFalsePositive(t *testing.T) {
	w := &Workers{rules: &mockRules{rules: "#6. 入职前 30 天是印象定型期。用这个时间建立关系网 + 找到不可替代的定位。不要急于出手。\n" +
		"#8. \"我们之前公司是这么做的\"是入职初期的社交毒药。"}, lastDecay: make(map[string]int64)}

	if count := w.countRuleConflicts(); count != 0 {
		t.Errorf("expected 0 conflicts on real-shaped rules, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// Start() — correctly launches goroutines
// ---------------------------------------------------------------------------

func TestWorkers_Start_LaunchesGoroutines(t *testing.T) {
	hub := &mockHub{}
	ts := newTaskStore(t)
	ts.Create("Startup task", "", "", "user", "", time.Now().Unix())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartWithTasks(ctx, nil, ts, nil, hub, nil)

	hub.waitForMessages(t, 1, 5*time.Second)

	if typ := broadcastType(hub.messages()); typ != "star-check" {
		t.Errorf("expected star-check broadcast, got %q", typ)
	}

	cancel()
}

func TestWorkers_Start_ContextCancellation(t *testing.T) {
	hub := &mockHub{}

	ctx, cancel := context.WithCancel(context.Background())

	Start(ctx, nil, hub, nil)

	time.Sleep(50 * time.Millisecond)

	cancel()

	time.Sleep(50 * time.Millisecond)
}

func TestWorkers_Start_StopsOnContext(t *testing.T) {
	hub := &mockHub{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	Start(ctx, nil, hub, nil)

	time.Sleep(10 * time.Millisecond)
}

// ---------------------------------------------------------------------------
// Memory decay tests
// ---------------------------------------------------------------------------

func TestWorkers_DecayAccumulatesAcrossTicks(t *testing.T) {
	ms := newMemStore(t)
	w := &Workers{memory: ms, lastDecay: make(map[string]int64)}

	old := time.Now().Add(-30 * 24 * time.Hour).UnixNano()
	e := memory.NewMemoryEntry(memory.MemoryType("fact"), "old idle memory", 0.9, "sess")
	e.DecayRate = 0.02
	e.CreatedAt = old
	saved, err := ms.Save(e)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	id := saved.ID

	w.lastDecay[id] = old

	w.decayMemories()
	after1, ok := ms.Get(id)
	if !ok {
		t.Fatalf("memory unexpectedly deleted after tick 1")
	}
	if after1.Confidence >= 0.9 {
		t.Fatalf("expected confidence to drop on tick 1, got %v", after1.Confidence)
	}

	w.lastDecay[id] = time.Now().Add(-30 * 24 * time.Hour).UnixNano()

	w.decayMemories()
	after2, ok := ms.Get(id)
	if !ok {
		return
	}
	if after2.Confidence >= after1.Confidence {
		t.Errorf("expected confidence to keep dropping across ticks; tick1=%v tick2=%v",
			after1.Confidence, after2.Confidence)
	}
}

func TestWorkers_DecaySkipsRecentlyDecayed(t *testing.T) {
	ms := newMemStore(t)
	w := &Workers{memory: ms, lastDecay: make(map[string]int64)}

	e := memory.NewMemoryEntry(memory.MemoryType("fact"), "fresh memory", 0.8, "sess")
	saved, err := ms.Save(e)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	w.decayMemories()
	after, ok := ms.Get(saved.ID)
	if !ok {
		t.Fatalf("memory unexpectedly deleted")
	}
	if after.Confidence != 0.8 {
		t.Errorf("expected no decay for recently-updated memory, got %v", after.Confidence)
	}
}

func TestWorkers_DecayConfirmedSlower(t *testing.T) {
	ms := newMemStore(t)
	w := &Workers{memory: ms, lastDecay: make(map[string]int64)}

	base := memory.NewMemoryEntry(memory.MemoryType("fact"), "unconfirmed", 0.9, "sess")
	base.DecayRate = 0.02
	unc, _ := ms.Save(base)

	confEntry := memory.NewMemoryEntry(memory.MemoryType("fact"), "confirmed", 0.9, "sess")
	confEntry.DecayRate = 0.02
	confEntry.Confirmed = true
	con, _ := ms.Save(confEntry)

	old := time.Now().Add(-10 * 24 * time.Hour).UnixNano()
	w.lastDecay[unc.ID] = old
	w.lastDecay[con.ID] = old

	w.decayMemories()

	uAfter, _ := ms.Get(unc.ID)
	cAfter, _ := ms.Get(con.ID)
	if !(cAfter.Confidence > uAfter.Confidence) {
		t.Errorf("expected confirmed memory to decay slower: confirmed=%v unconfirmed=%v",
			cAfter.Confidence, uAfter.Confidence)
	}
}

// ---------------------------------------------------------------------------
// mockRules — canned rule content for ReadDir, satisfying RuleReader.
// ---------------------------------------------------------------------------

type mockRules struct {
	rules string
}

func (m *mockRules) ReadDir(dir string) (string, error) {
	if dir == "rule" {
		return m.rules, nil
	}
	return "", nil
}

// Past-due detection must use LOCAL dates — UTC midnight truncation missed
// tasks overdue between local 00:00-08:00 on UTC+8. Dynamic dates pin the
// comparison to the device's local day boundary.
func TestWorkers_CountPastDueTasks_LocalDayBoundary(t *testing.T) {
	ts := newTaskStore(t)
	now := time.Now()
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	today := now.Format("2006-01-02")
	tomorrow := now.AddDate(0, 0, 1).Format("2006-01-02")
	ts.Create("due yesterday", "", yesterday, "user", "", time.Now().Unix())
	ts.Create("due today", "", today, "user", "", time.Now().Unix())
	ts.Create("due tomorrow", "", tomorrow, "user", "", time.Now().Unix())
	w := &Workers{tasks: ts, lastDecay: make(map[string]int64)}

	if count := w.countPastDueTasks(); count != 1 {
		t.Errorf("expected exactly the yesterday task to be past-due, got %d", count)
	}
}
