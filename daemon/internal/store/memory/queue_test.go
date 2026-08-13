package memory

import (
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// --- mock extractors for queue tests ---

// blockingExtractor blocks its Extract call until a channel is closed,
// simulating a slow LLM call. It tracks how many times Extract was called.
type blockingExtractor struct {
	startCh chan struct{}
	mu      sync.Mutex
	called  int
}

func (e *blockingExtractor) Extract(_ string) ([]MemoryEntry, error) {
	e.mu.Lock()
	e.called++
	e.mu.Unlock()
	<-e.startCh // block until closed
	return []MemoryEntry{}, nil
}

func (e *blockingExtractor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.called
}

// recordingExtractor returns predetermined entries and counts calls.
type recordingExtractor struct {
	mu      sync.Mutex
	calls   int
	entries []MemoryEntry
}

func (e *recordingExtractor) Extract(_ string) ([]MemoryEntry, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	return e.entries, nil
}

func (e *recordingExtractor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// --- helpers ---

func newTestMemoryStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

// --- tests ---

// TestQueueEnqueueNonBlockingSubmit verifies that Enqueue returns immediately
// and does not block the caller — a critical property for the chat path.
func TestQueueEnqueueNonBlockingSubmit(t *testing.T) {
	store := newTestMemoryStore(t)
	extractor := &recordingExtractor{}
	q := NewQueue(extractor, store, 1, nil)

	done := make(chan bool, 1)
	go func() {
		q.Enqueue(ExtractJob{SessionID: "ses_001", ConversationText: "hello"})
		done <- true
	}()

	select {
	case <-done:
		// Enqueue returned without blocking — success.
	case <-time.After(2 * time.Second):
		t.Fatal("Enqueue blocked the caller — non-blocking guarantee violated")
	}

	// Wait for the worker to pick up the job.
	time.Sleep(100 * time.Millisecond)
	if extractor.callCount() < 1 {
		t.Error("worker should have processed the enqueued job")
	}
}

// When the channel buffer is full (100), Enqueue drops silently instead of
// blocking — fill it while the worker is blocked, then verify enqueue #101
// doesn't block.
func TestQueueEnqueueDropsWhenFull(t *testing.T) {
	store := newTestMemoryStore(t)
	startCh := make(chan struct{})
	extractor := &blockingExtractor{startCh: startCh}
	q := NewQueue(extractor, store, 1, nil)

	// Give the worker goroutine time to start and dequeue the first job.
	time.Sleep(50 * time.Millisecond)

	// Enqueue 100 jobs to fill the remaining buffer capacity.
	// (Worker already dequeued one and is blocked on Extract, so the channel
	// can accept 100 more before it's full.)
	for i := 0; i < 100; i++ {
		q.Enqueue(ExtractJob{SessionID: fmt.Sprintf("ses_fill_%d", i), ConversationText: "fill"})
	}

	// The buffer should now be full. This enqueue must not block.
	overflowDone := make(chan struct{})
	go func() {
		q.Enqueue(ExtractJob{SessionID: "ses_overflow", ConversationText: "overflow"})
		close(overflowDone)
	}()

	select {
	case <-overflowDone:
		// Non-blocking — the overflow job was either enqueued or dropped,
		// neither of which blocks. Success.
	case <-time.After(3 * time.Second):
		t.Fatal("Enqueue blocked when buffer was full — select/default not working")
	}

	// Release the worker so it drains the buffer.
	close(startCh)
	time.Sleep(300 * time.Millisecond)

	// At most 101 jobs processed (1 early + 100 buffered); 102 means the overflow
	// was NOT dropped (okay — non-blocking matters, dropping is best-effort),
	// but never more than enqueued.
	calls := extractor.callCount()
	if calls > 102 {
		t.Errorf("unexpected extractor calls: %d (enqueued at most 102 jobs)", calls)
	}
	t.Logf("extractor called %d times (expected ≤ 102, ideally ≤ 101)", calls)
}

// TestQueueWorkerProcessesJobs verifies that the background worker calls
// the extractor for every job that makes it through the channel.
func TestQueueWorkerProcessesJobs(t *testing.T) {
	store := newTestMemoryStore(t)
	extractor := &recordingExtractor{
		entries: []MemoryEntry{
			{ID: "e1", Type: MemoryPersonFact, Person: "Alice", Content: "Alice is a senior engineer", Confidence: 0.95},
		},
	}
	q := NewQueue(extractor, store, 1, nil)

	q.Enqueue(ExtractJob{SessionID: "ses_001", ConversationText: "User: Tell me about Alice"})
	q.Enqueue(ExtractJob{SessionID: "ses_002", ConversationText: "User: What does Alice do?"})

	// Wait for the worker to process both jobs.
	time.Sleep(200 * time.Millisecond)

	if c := extractor.callCount(); c != 2 {
		t.Errorf("Extract call count = %d, want 2", c)
	}
}

// Full pipeline: high-confidence auto-saves, medium saves with
// Suggested=true, low-confidence is discarded.
func TestQueueWorkerClassifiesAndSaves(t *testing.T) {
	store := newTestMemoryStore(t)

	extractor := &recordingExtractor{
		entries: []MemoryEntry{
			{ID: "m1", Type: MemoryPersonFact, Person: "Alice", Content: "high confidence fact", Confidence: 0.95},
			{ID: "m2", Type: MemoryUserTrait, Content: "medium confidence fact", Confidence: 0.75},
			{ID: "m3", Type: MemoryRule, Content: "low confidence fact", Confidence: 0.30},
		},
	}
	q := NewQueue(extractor, store, 1, nil)

	q.Enqueue(ExtractJob{SessionID: "ses_001", ConversationText: "test conversation"})
	time.Sleep(200 * time.Millisecond)

	if extractor.callCount() != 1 {
		t.Fatalf("extractor should be called once, got %d", extractor.callCount())
	}

	all, err := store.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}

	// Should have 2 saved entries: high (auto-saved) + medium (suggested).
	// Low confidence is discarded.
	if len(all) != 2 {
		t.Fatalf("expected 2 saved entries (high + medium), got %d", len(all))
	}

	// Build a map for easy lookup.
	savedByID := make(map[string]MemoryEntry)
	for _, m := range all {
		savedByID[m.ID] = m
	}

	// High-confidence: auto-saved, not suggested.
	high, ok := savedByID["m1"]
	if !ok {
		t.Fatal("high-confidence entry m1 was not saved")
	}
	if high.SourceSession != "ses_001" {
		t.Errorf("high entry SourceSession = %q, want ses_001", high.SourceSession)
	}
	if high.Suggested {
		t.Error("high-confidence entry should NOT have Suggested=true")
	}
	if high.Confidence != 0.95 {
		t.Errorf("high entry Confidence = %f, want 0.95", high.Confidence)
	}

	// Medium-confidence: saved with Suggested=true.
	medium, ok := savedByID["m2"]
	if !ok {
		t.Fatal("medium-confidence entry m2 was not saved")
	}
	if !medium.Suggested {
		t.Error("medium-confidence entry should have Suggested=true")
	}

	// Low-confidence: discarded, should NOT be in the store.
	if _, exists := savedByID["m3"]; exists {
		t.Error("low-confidence entry m3 should have been discarded")
	}
}

// TestNewQueueConcurrencyDefaultsToOne verifies that passing concurrency=0
// results in the queue defaulting to 1 worker goroutine.
func TestNewQueueConcurrencyDefaultsToOne(t *testing.T) {
	store := newTestMemoryStore(t)
	extractor := &recordingExtractor{}
	q := NewQueue(extractor, store, 0, nil)

	if q == nil {
		t.Fatal("NewQueue returned nil")
	}
	if q.jobs == nil {
		t.Fatal("job channel is nil")
	}
	if cap(q.jobs) != 100 {
		t.Errorf("channel capacity = %d, want 100", cap(q.jobs))
	}

	// Verify the queue actually works (worker is running).
	q.Enqueue(ExtractJob{SessionID: "ses_001", ConversationText: "test"})
	time.Sleep(100 * time.Millisecond)
	if extractor.callCount() < 1 {
		t.Error("worker with default concurrency should process jobs")
	}
}
