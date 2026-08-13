package notify

import (
	"context"
	"testing"
	"time"
)

func TestQueuePushPoll(t *testing.T) {
	q := NewQueue(8)
	q.PushFields("star-alert", "r1", "T", "B", "home")
	got := q.Poll(context.Background(), time.Second)
	if len(got) != 1 || got[0].Type != "star-alert" || got[0].TargetTab != "home" {
		t.Fatalf("unexpected poll result: %+v", got)
	}
	// Drained — a second poll should time out empty quickly.
	got2 := q.Poll(context.Background(), 50*time.Millisecond)
	if got2 != nil {
		t.Errorf("expected empty after drain, got %+v", got2)
	}
}

func TestQueueLongPollWakesOnPush(t *testing.T) {
	q := NewQueue(8)
	done := make(chan []Payload, 1)
	go func() { done <- q.Poll(context.Background(), 2*time.Second) }()
	time.Sleep(30 * time.Millisecond)
	q.PushFields("memory-confirm", "m1", "T", "B", "")
	select {
	case got := <-done:
		if len(got) != 1 {
			t.Fatalf("long-poll should wake with 1 item, got %d", len(got))
		}
	case <-time.After(time.Second):
		t.Fatal("long-poll did not wake on push")
	}
}

func TestQueueDropsOldestWhenFull(t *testing.T) {
	q := NewQueue(2)
	q.PushFields("t", "1", "a", "", "")
	q.PushFields("t", "2", "b", "", "")
	q.PushFields("t", "3", "c", "", "") // evicts id 1
	got := q.Poll(context.Background(), time.Second)
	if len(got) != 2 || got[0].ID != "2" || got[1].ID != "3" {
		t.Fatalf("expected [2,3] after eviction, got %+v", got)
	}
}

func TestQueuePollCancel(t *testing.T) {
	q := NewQueue(8)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	got := q.Poll(ctx, 5*time.Second)
	if got != nil {
		t.Errorf("cancelled poll should return nil, got %+v", got)
	}
}
