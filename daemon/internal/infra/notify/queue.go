// Package notify bridges background workers and the Android foreground service
// via long-poll — no Go→JVM JNI callbacks, and it works even when WebView is closed.
package notify

import (
	"context"
	"sync"
	"time"
)

// Payload is the notification schema shared with the Android NotificationHelper.
type Payload struct {
	Type         string `json:"type"`
	ID           string `json:"id"`
	Title        string `json:"title"`
	Body         string `json:"body"`
	Urgency      string `json:"urgency,omitempty"`
	TargetTab    string `json:"target_tab,omitempty"`
	TargetAnchor string `json:"target_anchor,omitempty"`
}

type Queue struct {
	mu     sync.Mutex
	items  []Payload
	max    int
	signal chan struct{}
}

func NewQueue(max int) *Queue {
	if max < 1 {
		max = 32
	}
	return &Queue{max: max, signal: make(chan struct{}, 1)}
}

func (q *Queue) Push(p Payload) {
	q.mu.Lock()
	if len(q.items) >= q.max {
		q.items = q.items[1:]
	}
	q.items = append(q.items, p)
	q.mu.Unlock()

	select {
	case q.signal <- struct{}{}:
	default:
	}
}

// PushFields matches background.Notifier so callers don't need this package's Payload type.
func (q *Queue) PushFields(typ, id, title, body, targetTab string) {
	q.Push(Payload{Type: typ, ID: id, Title: title, Body: body, TargetTab: targetTab})
}

func (q *Queue) drain() []Payload {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return nil
	}
	out := q.items
	q.items = nil
	return out
}

func (q *Queue) Poll(ctx context.Context, timeout time.Duration) []Payload {
	if items := q.drain(); items != nil {
		return items
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-q.signal:
		return q.drain()
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return nil
	}
}
