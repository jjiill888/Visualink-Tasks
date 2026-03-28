package hub

import "sync"

// Hub manages a set of SSE client channels and broadcasts events to all of them.
type Hub struct {
	mu      sync.Mutex
	clients map[chan string]struct{}
}

// Global is the singleton hub used by all handlers.
var Global = &Hub{
	clients: make(map[chan string]struct{}),
}

// Subscribe returns a new channel that will receive broadcast messages.
// The caller must call Unsubscribe when done (e.g. on client disconnect).
func (h *Hub) Subscribe() chan string {
	ch := make(chan string, 4)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

// Unsubscribe removes and closes a channel previously returned by Subscribe.
func (h *Hub) Unsubscribe(ch chan string) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
	close(ch)
}

// Broadcast sends event to every connected client (non-blocking).
func (h *Hub) Broadcast(event string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- event:
		default: // slow client — skip rather than block
		}
	}
}
