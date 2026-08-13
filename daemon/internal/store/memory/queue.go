package memory

import (
	"fmt"
	"log"
	"sync"
)

type ExtractJob struct {
	SessionID        string
	ConversationText string
}

// MemoryNotifier is the subset of notify.Queue used here, avoiding a circular import.
type MemoryNotifier interface {
	PushFields(typ, id, title, body, targetTab string)
}

// Queue keeps memory extraction async and bounded, decoupled from the chat path.
type Queue struct {
	extractor MemoryExtractor
	store     *Store
	jobs      chan ExtractJob
	wg        sync.WaitGroup
	// closeOnce makes Stop/WaitForIdle idempotent — closing a closed channel panics.
	closeOnce sync.Once
	// closed makes Enqueue a no-op after Stop (a send on a closed channel panics even
	// via select-default); recover() below turns any remaining race into a dropped job.
	closedMu sync.RWMutex
	closed   bool
	// saveMu serializes check-then-save so two workers can't double-insert a fact.
	saveMu   sync.Mutex
	notifier MemoryNotifier
}

// NewQueue starts N workers over a bounded channel (capacity 100); when full, jobs
// are dropped so no back-pressure reaches the chat path. notifier is optional.
func NewQueue(extractor MemoryExtractor, store *Store, concurrency int, notifier MemoryNotifier) *Queue {
	if concurrency < 1 {
		concurrency = 1
	}
	q := &Queue{
		extractor: extractor,
		store:     store,
		jobs:      make(chan ExtractJob, 100),
		notifier:  notifier,
	}
	for i := 0; i < concurrency; i++ {
		q.wg.Add(1)
		go q.worker()
	}
	return q
}

// Enqueue submits a job non-blocking: if the buffer is full (or the queue is
// stopped) the job is silently dropped so the chat path is never delayed.
func (q *Queue) Enqueue(job ExtractJob) {
	q.closedMu.RLock()
	closed := q.closed
	q.closedMu.RUnlock()
	if closed {
		return // queue shut down; drop silently
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("memory: extraction queue closed mid-send, dropping job: %v", r)
		}
	}()
	select {
	case q.jobs <- job:
	default:
		log.Printf("memory: extraction queue full, dropping job for session %s", job.SessionID)
	}
}

func (q *Queue) worker() {
	defer q.wg.Done()
	for job := range q.jobs {
		entries, err := q.extractor.Extract(job.ConversationText)
		if err != nil {
			log.Printf("memory: extraction error for session %s: %v", job.SessionID, err)
			continue
		}
		if len(entries) == 0 {
			continue
		}

		decision := ClassifyConfidence(entries)

		saved := 0
		if q.store != nil {
			for _, e := range decision.HighConfidence {
				e.SourceSession = job.SessionID
				if q.saveDeduped(e) {
					saved++
				}
			}
			for _, e := range decision.MediumConfidence {
				e.SourceSession = job.SessionID
				e.Suggested = true
				if q.saveDeduped(e) {
					saved++
				}
			}
		}

		// Notify so the learning loop is visible rather than a black box.
		if saved > 0 && q.notifier != nil {
			var body string
			if saved == 1 {
				body = "1 memory extracted — tap to review"
			} else {
				body = fmt.Sprintf("%d memories extracted — tap to review", saved)
			}
			q.notifier.PushFields("memories-extracted", job.SessionID, "Memory", body, "context")
		}
	}
}

// saveDeduped saves a memory only if no near-duplicate exists (extraction runs over
// overlapping windows, so without this the same fact would be stored many times).
// Returns true only when actually persisted.
func (q *Queue) saveDeduped(e MemoryEntry) bool {
	if q.store == nil {
		return false
	}
	// Serialize the whole check-then-save: reads then Save are separate DB calls, so
	// with >1 worker two jobs could both pass the dedup check before either inserts
	// (SetMaxOpenConns(1) only serializes individual statements).
	q.saveMu.Lock()
	defer q.saveMu.Unlock()
	// Cheap exact-match pass: substring LIKE over-pruned (a short fact suppressed any
	// later fact containing it); semantic dedup handles paraphrase elimination.
	if similar, err := q.store.FindSimilarByContent(e.Content, 5); err == nil {
		for _, s := range similar {
			if s.Person == e.Person && s.Type == e.Type &&
				s.Content == e.Content && !opposingPolarity(e.Content, s.Content) {
				return false // exact duplicate already stored
			}
		}
	}
	// Semantic pass: polarity-aware topic overlap on same-person memories — dedup a
	// reworded non-contradictory fact, keep a corrected (opposite-polarity) one.
	if e.Person != "" {
		if existing, err := q.store.ListByPerson(e.Person); err == nil {
			for _, ex := range existing {
				if ex.Type == e.Type && sharedWordsCount(e.Content, ex.Content, e.Person) >= 2 && !opposingPolarity(e.Content, ex.Content) {
					return false // same-person, same-type, overlapping, non-contradictory → dup
				}
			}
		}
	}
	if _, err := q.store.Save(e); err != nil {
		log.Printf("memory queue: save entry: %v", err)
		return false
	}
	return true
}

// Stop closes the job channel and drains in-flight workers. Idempotent.
func (q *Queue) Stop() {
	q.closedMu.Lock()
	q.closed = true
	q.closedMu.Unlock()
	q.closeOnce.Do(func() { close(q.jobs) })
	q.wg.Wait()
}

// WaitForIdle drains the queue deterministically for tests. After it returns the
// queue cannot be reused. Idempotent — safe alongside Stop.
func (q *Queue) WaitForIdle() {
	q.closeOnce.Do(func() { close(q.jobs) })
	q.wg.Wait()
}
