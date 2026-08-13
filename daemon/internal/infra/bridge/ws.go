package bridge

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"sync"

	"nhooyr.io/websocket"
)

type Client struct {
	ID   string
	Send chan []byte
}

type Hub struct {
	mu      sync.RWMutex
	clients map[string]*Client
}

func NewHub() *Hub {
	return &Hub{
		clients: make(map[string]*Client),
	}
}

func (h *Hub) Register(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c.ID] = c
}

func (h *Hub) Unregister(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, id)
}

func (h *Hub) Broadcast(msg []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, c := range h.clients {
		select {
		case c.Send <- msg:
		default:
			// client buffer full, skip
		}
	}
}

func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// maxWSClients bounds concurrent WebSocket connections (2 goroutines + a
// 64-slot channel each) — beyond the app's single use, it's a bug or local abuse.
const maxWSClients = 32

func HandleWS(hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if hub.Count() >= maxWSClients {
			http.Error(w, "too many websocket connections", http.StatusServiceUnavailable)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			// InsecureSkipVerify: local loopback only, no TLS cert validation needed
			InsecureSkipVerify: true,
		})
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")

		client := &Client{
			ID:   genWSID(),
			Send: make(chan []byte, 64),
		}
		hub.Register(client)
		defer func() {
			hub.Unregister(client.ID)
			close(client.Send)
		}()

		// connCtx is cancelled when the read pump exits, signalling the write pump
		// to stop even if the request context is still live.
		connCtx, connCancel := context.WithCancel(r.Context())
		defer connCancel()

		// Write pump exits on send error, closed channel, or connCtx cancellation.
		writeDone := make(chan struct{})
		go func() {
			defer close(writeDone)
			for {
				select {
				case msg, ok := <-client.Send:
					if !ok {
						return
					}
					if err := conn.Write(connCtx, websocket.MessageText, msg); err != nil {
						return
					}
				case <-connCtx.Done():
					return
				}
			}
		}()

		for {
			_, _, err := conn.Read(connCtx)
			if err != nil {
				break
			}
		}
		connCancel()
		<-writeDone
	}
}

func genWSID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("ws_%x", b)
}
