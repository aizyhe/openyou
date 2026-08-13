package bridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

func TestNewHub(t *testing.T) {
	h := NewHub()
	if h == nil {
		t.Fatal("NewHub should return a non-nil Hub")
	}
	if h.clients == nil {
		t.Error("Hub.clients should be initialized")
	}
	if h.Count() != 0 {
		t.Errorf("New hub should have 0 clients, got %d", h.Count())
	}
}

func TestRegister(t *testing.T) {
	h := NewHub()

	c := &Client{ID: "ws_abc123", Send: make(chan []byte, 64)}
	h.Register(c)

	if h.Count() != 1 {
		t.Errorf("After Register, Count = %d, want 1", h.Count())
	}

	// Register same ID again (overwrite).
	c2 := &Client{ID: "ws_abc123", Send: make(chan []byte, 64)}
	h.Register(c2)

	if h.Count() != 1 {
		t.Errorf("After re-register with same ID, Count = %d, want 1", h.Count())
	}
}

func TestRegisterMultipleClients(t *testing.T) {
	h := NewHub()

	for i := 0; i < 5; i++ {
		c := &Client{ID: "ws_client_" + string(rune('0'+i)), Send: make(chan []byte, 64)}
		h.Register(c)
	}

	if h.Count() != 5 {
		t.Errorf("After 5 registrations, Count = %d, want 5", h.Count())
	}
}

func TestUnregister(t *testing.T) {
	h := NewHub()

	c := &Client{ID: "ws_abc123", Send: make(chan []byte, 64)}
	h.Register(c)

	if h.Count() != 1 {
		t.Fatalf("Expected 1 client after register, got %d", h.Count())
	}

	h.Unregister("ws_abc123")

	if h.Count() != 0 {
		t.Errorf("After Unregister, Count = %d, want 0", h.Count())
	}
}

func TestUnregisterNonExistent(t *testing.T) {
	h := NewHub()

	// Should not panic.
	h.Unregister("nonexistent")

	if h.Count() != 0 {
		t.Errorf("Count should stay 0 after unregistering nonexistent, got %d", h.Count())
	}
}

func TestCount(t *testing.T) {
	h := NewHub()

	if h.Count() != 0 {
		t.Errorf("Initial Count = %d, want 0", h.Count())
	}

	// Register, check count.
	c1 := &Client{ID: "ws_1", Send: make(chan []byte, 64)}
	h.Register(c1)
	if h.Count() != 1 {
		t.Errorf("Count after 1 register = %d, want 1", h.Count())
	}

	// Register another, check count.
	c2 := &Client{ID: "ws_2", Send: make(chan []byte, 64)}
	h.Register(c2)
	if h.Count() != 2 {
		t.Errorf("Count after 2 registers = %d, want 2", h.Count())
	}

	// Unregister, check count.
	h.Unregister("ws_1")
	if h.Count() != 1 {
		t.Errorf("Count after 1 unregister = %d, want 1", h.Count())
	}

	// Unregister last.
	h.Unregister("ws_2")
	if h.Count() != 0 {
		t.Errorf("Count after all unregistered = %d, want 0", h.Count())
	}
}

func TestBroadcastToEmptyHub(t *testing.T) {
	h := NewHub()

	// Broadcast to empty hub should not panic.
	h.Broadcast([]byte("hello"))
}

func TestBroadcastToSingleClient(t *testing.T) {
	h := NewHub()

	ch := make(chan []byte, 64)
	c := &Client{ID: "ws_1", Send: ch}
	h.Register(c)

	msg := []byte("hello world")
	h.Broadcast(msg)

	select {
	case received := <-ch:
		if string(received) != string(msg) {
			t.Errorf("Broadcast received %q, want %q", string(received), string(msg))
		}
	default:
		t.Error("Broadcast should have delivered message to client")
	}
}

func TestBroadcastToMultipleClients(t *testing.T) {
	h := NewHub()

	var chs []chan []byte
	for i := 0; i < 5; i++ {
		ch := make(chan []byte, 64)
		chs = append(chs, ch)
		h.Register(&Client{ID: "ws_" + string(rune('0'+i)), Send: ch})
	}

	msg := []byte("broadcast to all")
	h.Broadcast(msg)

	for i, ch := range chs {
		select {
		case received := <-ch:
			if string(received) != string(msg) {
				t.Errorf("Client %d received %q, want %q", i, string(received), string(msg))
			}
		default:
			t.Errorf("Client %d did not receive broadcast", i)
		}
	}
}

func TestBroadcastToSomeFullBuffers(t *testing.T) {
	h := NewHub()

	// Client 1: buffer size 1 (will fill).
	ch1 := make(chan []byte, 1)
	c1 := &Client{ID: "ws_1", Send: ch1}
	h.Register(c1)

	// Client 2: buffer size 64 (won't fill).
	ch2 := make(chan []byte, 64)
	c2 := &Client{ID: "ws_2", Send: ch2}
	h.Register(c2)

	// Fill client 1's buffer.
	ch1 <- []byte("filler")

	// Broadcast — client 1's buffer is full, so it gets skipped.
	msg := []byte("important message")
	h.Broadcast(msg)

	// Client 2 should receive it.
	select {
	case received := <-ch2:
		if string(received) != string(msg) {
			t.Errorf("Client 2 received %q, want %q", string(received), string(msg))
		}
	default:
		t.Error("Client 2 should have received broadcast")
	}

	// Client 1 should still have the filler (broadcast was skipped).
	select {
	case filler := <-ch1:
		if string(filler) != "filler" {
			t.Errorf("Client 1 filler = %q, want filler", string(filler))
		}
	default:
		t.Error("Client 1 should have filler message")
	}

	// Client 1 should NOT have the broadcast message.
	select {
	case <-ch1:
		t.Error("Client 1 should not have received broadcast (buffer was full)")
	default:
		// Expected: no message.
	}
}

func TestBroadcastAfterUnregister(t *testing.T) {
	h := NewHub()

	ch := make(chan []byte, 64)
	c := &Client{ID: "ws_1", Send: ch}
	h.Register(c)

	// Unregister.
	h.Unregister("ws_1")

	// Broadcast to empty hub.
	h.Broadcast([]byte("nobody should receive this"))

	// Client should not receive anything (but channel is still open).
	select {
	case <-ch:
		t.Error("Unregistered client should not receive broadcast")
	default:
		// Expected.
	}
}

func TestRegisterUnregisterRace(t *testing.T) {
	h := NewHub()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ch := make(chan []byte, 1)
			c := &Client{ID: "ws_race_" + string(rune('0'+id%10)), Send: ch}
			h.Register(c)
			h.Unregister(c.ID)
		}(i)
	}
	wg.Wait()

	// After all goroutines finish, hub should have at most 10 unique clients
	// (some may remain if register/unregister interleaved in unexpected ways).
	count := h.Count()
	if count > 10 {
		t.Errorf("After register/unregister race, Count = %d, want <= 10", count)
	}
}

func TestBroadcastRace(t *testing.T) {
	h := NewHub()

	// Register some clients.
	for i := 0; i < 5; i++ {
		ch := make(chan []byte, 128)
		h.Register(&Client{ID: "ws_" + string(rune('0'+i)), Send: ch})
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.Broadcast([]byte("race test"))
		}()
	}
	wg.Wait()
	// No panic = pass.
}

func TestGenWSID(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := genWSID()
		if ids[id] {
			t.Errorf("genWSID generated duplicate ID: %s", id)
		}
		ids[id] = true

		// ID should have prefix "ws_".
		if len(id) < 3 || id[:3] != "ws_" {
			t.Errorf("genWSID should start with 'ws_', got %q", id)
		}
	}
}

func TestGenWSIDConcurrently(t *testing.T) {
	var mu sync.Mutex
	ids := make(map[string]bool)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				id := genWSID()
				mu.Lock()
				if ids[id] {
					t.Errorf("concurrent genWSID duplicate: %s", id)
				}
				ids[id] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(ids) != 500 {
		t.Errorf("Expected 500 unique IDs from 50*10 concurrent calls, got %d", len(ids))
	}
}

// TestHandleWS_EndToEnd drives a real WebSocket connection through HandleWS so
// the accept, register, read pump, and write pump are all exercised.
func TestHandleWS_EndToEnd(t *testing.T) {
	hub := NewHub()
	srv := httptest.NewServer(HandleWS(hub))
	defer srv.Close()

	wsURL := "ws" + srv.URL[len("http"):]

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Wait until the server has registered the client.
	waitFor(t, func() bool { return hub.Count() == 1 })

	// Broadcast a message; the write pump must deliver it to the client.
	want := []byte("hello from server")
	hub.Broadcast(want)

	if _, got, err := conn.Read(ctx); err != nil {
		t.Fatalf("Read: %v", err)
	} else if string(got) != string(want) {
		t.Errorf("received %q, want %q", got, want)
	}

	// Client → server: exercise the read pump reading a client message.
	if err := conn.Write(ctx, websocket.MessageText, []byte("ping")); err != nil {
		t.Fatalf("client Write: %v", err)
	}

	// Close the client; the server read pump should break and unregister.
	conn.Close(websocket.StatusNormalClosure, "bye")
	waitFor(t, func() bool { return hub.Count() == 0 })
}

// TestHandleWS_AcceptError hits the accept-error return path: a plain HTTP GET
// without WebSocket upgrade headers cannot be accepted.
func TestHandleWS_AcceptError(t *testing.T) {
	hub := NewHub()
	srv := httptest.NewServer(HandleWS(hub))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	resp.Body.Close()
	// websocket.Accept writes a 400 when the upgrade fails; handler returns
	// without registering anything.
	if hub.Count() != 0 {
		t.Errorf("no client should be registered on failed accept, got %d", hub.Count())
	}
}

// TestHandleWS_WritePumpError closes the connection while a queued broadcast is
// pending so the write pump's conn.Write returns an error and the goroutine
// exits cleanly.
func TestHandleWS_MultipleClients(t *testing.T) {
	hub := NewHub()
	srv := httptest.NewServer(HandleWS(hub))
	defer srv.Close()
	wsURL := "ws" + srv.URL[len("http"):]

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var conns []*websocket.Conn
	for i := 0; i < 3; i++ {
		c, _, err := websocket.Dial(ctx, wsURL, nil)
		if err != nil {
			t.Fatalf("Dial %d: %v", i, err)
		}
		conns = append(conns, c)
	}
	waitFor(t, func() bool { return hub.Count() == 3 })

	hub.Broadcast([]byte("to all"))
	for i, c := range conns {
		if _, got, err := c.Read(ctx); err != nil {
			t.Fatalf("conn %d Read: %v", i, err)
		} else if string(got) != "to all" {
			t.Errorf("conn %d got %q", i, got)
		}
	}

	for _, c := range conns {
		c.Close(websocket.StatusNormalClosure, "")
	}
	waitFor(t, func() bool { return hub.Count() == 0 })
}

// waitFor polls cond up to ~2s with tiny sleeps (deterministic, no fixed waits).
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}
