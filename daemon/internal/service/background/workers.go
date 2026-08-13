package background

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"openyou/daemon/internal/store/memory"
	"openyou/daemon/internal/store/task"
)

// confirmedFloor is the minimum confidence confirmed memories decay to (never auto-deleted).
const confirmedFloor = 0.5

// decayGraceCap caps how far back the first-sighting decay reference goes, so a
// memory restored from an old backup doesn't take its whole shelf-time decay at once.
const decayGraceCap = 7 * 24 * time.Hour

type Broadcaster interface {
	Broadcast(msg []byte)
}

// Notifier enqueues an OS notification; kept minimal so background doesn't import notify.
type Notifier interface {
	PushFields(typ, id, title, body, targetTab string)
}

type RuleReader interface {
	ReadDir(dir string) (string, error)
}

type Workers struct {
	memory   *memory.Store
	tasks    *task.Store
	rules    RuleReader
	hub      Broadcaster
	notifier Notifier

	hashMu         sync.Mutex
	lastStarHash   string
	lastHealthHash string

	// lastDecay tracks per-memory last decay time (wall clock). Needed because
	// UpdateConfidence rewrites updated_at each write, which would otherwise reset
	// the decay clock and stop confidence from decaying over long idle periods.
	decayMu   sync.Mutex
	lastDecay map[string]int64
}

func Start(ctx context.Context, m *memory.Store, h Broadcaster, n Notifier) {
	StartWithTasks(ctx, m, nil, nil, h, n)
}

func StartWithTasks(ctx context.Context, m *memory.Store, t *task.Store, r RuleReader, h Broadcaster, n Notifier) {
	w := &Workers{memory: m, tasks: t, rules: r, hub: h, notifier: n, lastDecay: make(map[string]int64)}
	go w.starLoop(ctx)
	go w.insideHealthLoop(ctx)
	go w.memDecayLoop(ctx)
}

func (w *Workers) notify(typ, id, title, body, targetTab string) {
	if w.notifier != nil {
		w.notifier.PushFields(typ, id, title, body, targetTab)
	}
}

func (w *Workers) starLoop(ctx context.Context) {
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()

	w.checkStar()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.checkStar()
		}
	}
}

func (w *Workers) checkStar() {
	if w.tasks == nil {
		return
	}
	tasks, err := w.tasks.List()
	if err != nil {
		log.Printf("background: star check: list tasks: %v", err)
		return
	}
	pending := 0
	var b strings.Builder
	for _, t := range tasks {
		if t.Done {
			continue
		}
		pending++
		b.WriteString(t.Title)
		if t.EndDate != "" {
			b.WriteString("(" + t.EndDate + ")")
		}
		b.WriteString(";")
	}
	if pending == 0 {
		return
	}

	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(b.String())))
	w.hashMu.Lock()
	changed := hash != w.lastStarHash
	if changed {
		w.lastStarHash = hash
	}
	w.hashMu.Unlock()
	if !changed {
		return // nothing changed
	}

	msg, _ := json.Marshal(map[string]interface{}{
		"type":    "star-check",
		"pending": pending,
		"time":    time.Now().Unix(),
	})
	w.hub.Broadcast(msg)

	w.notify("star-alert", "star-alert",
		fmt.Sprintf("%d 项待办任务", pending),
		"打开查看待跟进事项", "time")
}

// contradictoryPairs are phrase pairs that, when both present in rules, suggest
// conflicting advice; kept conservative (specific phrasings) to avoid false positives.
var contradictoryPairs = [][2]string{
	// English
	{"confront directly", "avoid confrontation"},
	{"speak up", "stay silent"},
	{"speak up", "keep quiet"},
	{"call out publicly", "handle privately"},
	{"escalate to manager", "resolve at your level"},
	{"take initiative", "wait for approval"},
	{"be aggressive", "be passive"},
	{"push back", "accept and move on"},
	// Chinese (corresponding expressions)
	{"正面冲突", "避免冲突"},
	{"主动发声", "保持沉默"},
	{"公开对质", "私下处理"},
	{"上报领导", "自己解决"},
	{"主动出击", "等待批准"},
}

func (w *Workers) insideHealthLoop(ctx context.Context) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	w.checkInsideHealth()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.checkInsideHealth()
		}
	}
}

func (w *Workers) readDocs(dir string) string {
	if w.rules == nil {
		return ""
	}
	if r, err := w.rules.ReadDir(dir); err == nil {
		return r
	}
	return ""
}

func (w *Workers) checkInsideHealth() {
	conflicts := w.countRuleConflicts()
	pastDue := w.countPastDueTasks()

	// Nothing to report
	if conflicts == 0 && pastDue == 0 {
		return
	}

	// Dedup based on combined numeric state
	payload := fmt.Sprintf("%d,%d", conflicts, pastDue)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
	w.hashMu.Lock()
	changed := hash != w.lastHealthHash
	if changed {
		w.lastHealthHash = hash
	}
	w.hashMu.Unlock()
	if !changed {
		return
	}

	var issues []string
	if conflicts > 0 {
		issues = append(issues, fmt.Sprintf("%d potential rule conflicts detected", conflicts))
	}
	if pastDue > 0 {
		issues = append(issues, fmt.Sprintf("%d tasks are past their due date", pastDue))
	}

	msg, _ := json.Marshal(map[string]interface{}{
		"type":           "context-health",
		"rule_conflicts": conflicts,
		"past_due_tasks": pastDue,
		"issues":         issues,
		"time":           time.Now().Unix(),
	})
	w.hub.Broadcast(msg)

	if len(issues) > 0 {
		w.notify("health-warning", "health-warning",
			"资料需要检查", strings.Join(issues, "；"), "context")
	}
}

func (w *Workers) countRuleConflicts() int {
	rules := w.readDocs("rule")
	if rules == "" {
		return 0
	}
	lower := strings.ToLower(rules)
	count := 0
	for _, pair := range contradictoryPairs {
		if strings.Contains(lower, pair[0]) && strings.Contains(lower, pair[1]) {
			count++
		}
	}
	return count
}

func (w *Workers) countPastDueTasks() int {
	if w.tasks == nil {
		return 0
	}
	tasks, err := w.tasks.List()
	if err != nil {
		return 0
	}
	// Compare calendar dates in the device's LOCAL timezone — Truncate(24h) uses
	// UTC midnight and would miss tasks overdue between local 00:00-08:00 at UTC+8.
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	count := 0
	for _, t := range tasks {
		if t.Done || t.EndDate == "" {
			continue
		}
		d, err := time.ParseInLocation("2006-01-02", t.EndDate, time.Local)
		if err == nil && d.Before(today) {
			count++
		}
	}
	return count
}

func (w *Workers) memDecayLoop(ctx context.Context) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	w.decayMemories()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.decayMemories()
		}
	}
}

func (w *Workers) decayMemories() {
	if w.memory == nil {
		return
	}

	entries, err := w.memory.ListAll()
	if err != nil {
		log.Printf("background: memory decay: list all: %v", err)
		return
	}

	now := time.Now()
	nowNanos := now.UnixNano()
	deleted := 0
	updated := 0

	w.decayMu.Lock()
	if w.lastDecay == nil {
		w.lastDecay = make(map[string]int64)
	}
	// Snapshot lastDecay under the lock so the I/O loop below reads it without
	// re-acquiring the mutex on every iteration.
	live := make(map[string]bool, len(entries))
	lastDecaySnap := make(map[string]int64, len(w.lastDecay))
	for id, t := range w.lastDecay {
		lastDecaySnap[id] = t
	}
	w.decayMu.Unlock()

	// decayUpdates batch all lastDecay writes into one lock acquisition after the loop.
	decayUpdates := make(map[string]int64)

	for _, e := range entries {
		live[e.ID] = true

		// Confirmed memories decay much slower (half rate).
		rate := e.DecayRate
		// Clamp rate to [0,1): a corrupt backup value >1 or negative would make
		// 1-rate negative (math.Pow → NaN) or grow confidence without bound.
		if rate < 0 || math.IsNaN(rate) {
			rate = 0
		} else if rate > 0.99 {
			rate = 0.99
		}
		if e.Confirmed {
			rate = rate * 0.5
		}

		// Reference time for decay: first sighting seeds from UpdatedAt (falling back
		// to CreatedAt); later ticks use the recorded last-decay time, which is NOT
		// reset by UpdateConfidence's updated_at rewrite.
		last, seen := lastDecaySnap[e.ID]
		if !seen {
			last = e.UpdatedAt
			if last == 0 {
				last = e.CreatedAt
			}
			if last == 0 {
				last = nowNanos
			}
			// Cap the first-sighting reference to decayGraceCap back, or a restored
			// old backup would take its entire shelf-time decay in one tick.
			if minLast := nowNanos - int64(decayGraceCap); last < minLast {
				last = minLast
			}
			decayUpdates[e.ID] = last
		}

		elapsed := time.Duration(nowNanos-last) * time.Nanosecond
		daysFloat := elapsed.Hours() / 24
		if daysFloat < 1 {
			continue // Don't decay memories idle for less than 1 day
		}

		// Exponential decay conf*(1-rate)^days never goes negative, unlike the
		// linear form which wiped a memory in one tick after a long idle gap.
		factor := math.Pow(1-rate, daysFloat)
		newConfidence := e.Confidence * factor

		// Confirmed memories are user-approved — never auto-delete; floor their
		// confidence so they persist until the user removes them.
		if e.Confirmed {
			if newConfidence < confirmedFloor {
				newConfidence = confirmedFloor
			}
			if err := w.memory.UpdateConfidence(e.ID, newConfidence); err != nil {
				log.Printf("background: memory decay: update %s: %v", e.ID, err)
			} else {
				updated++
				decayUpdates[e.ID] = nowNanos
			}
			continue
		}

		if newConfidence < 0.1 {
			if err := w.memory.Delete(e.ID); err != nil {
				log.Printf("background: memory decay: delete %s: %v", e.ID, err)
			} else {
				deleted++
				decayUpdates[e.ID] = -1 // sentinel: remove from lastDecay
			}
		} else {
			if err := w.memory.UpdateConfidence(e.ID, newConfidence); err != nil {
				log.Printf("background: memory decay: update %s: %v", e.ID, err)
			} else {
				updated++
				// Measure next tick's elapsed time from here, not from updated_at.
				decayUpdates[e.ID] = nowNanos
			}
		}
	}

	// Apply batched lastDecay mutations in one lock acquisition, pruning dead IDs.
	w.decayMu.Lock()
	for id, ts := range decayUpdates {
		if ts == -1 {
			delete(w.lastDecay, id)
		} else {
			w.lastDecay[id] = ts
		}
	}
	for id := range w.lastDecay {
		if !live[id] {
			delete(w.lastDecay, id)
		}
	}
	w.decayMu.Unlock()

	if deleted > 0 || updated > 0 {
		log.Printf("background: memory decay: updated %d, deleted %d (of %d)", updated, deleted, len(entries))
	}
}
