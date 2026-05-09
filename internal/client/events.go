package client

import (
	"sync"
	"sync/atomic"
	"time"
)

// localEvent is one entry on the internal event bus. cmd/agent's local-API
// SSE handler subscribes to this hub and re-marshals to JSON for clients.
//
// This is an UNEXPORTED type (lowercase l), so its `Data map[string]any`
// field doesn't violate the FFI constraint rules in api_constraints_test.go
// (those rules only apply to exported types). The exported `Event` type in
// api.go has a constrained string-only payload.
type localEvent struct {
	Time time.Time      `json:"time"`
	Type string         `json:"type"` // "status" | "peers" | "error" | "log"
	Data map[string]any `json:"data"`
}

// localEventHub is a tiny pub/sub for in-process subscribers (cmd/agent's
// SSE handler is the canonical consumer). Each subscriber gets its own
// buffered channel; if a subscriber falls behind beyond the buffer, we
// drop the event for that client (never block the producer).
type localEventHub struct {
	mu          sync.Mutex
	subscribers map[chan localEvent]struct{}
}

// globalEventBus is set when the local-API server starts so other parts
// of the package (watchdogs, restart callbacks) can publish events
// without holding a reference to the local-API server struct. Nil if
// the bus hasn't been initialized — publishToEventBus then no-ops.
var globalEventBus atomic.Pointer[localEventHub]

// publishToEventBus emits an event on the package-level bus if one has
// been initialized. Safe to call from any goroutine; never blocks.
func publishToEventBus(ev localEvent) {
	if hub := globalEventBus.Load(); hub != nil {
		hub.publish(ev)
	}
}

func newLocalEventHub() *localEventHub {
	return &localEventHub{subscribers: make(map[chan localEvent]struct{})}
}

func (h *localEventHub) subscribe() chan localEvent {
	ch := make(chan localEvent, 32)
	h.mu.Lock()
	h.subscribers[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *localEventHub) unsubscribe(ch chan localEvent) {
	h.mu.Lock()
	delete(h.subscribers, ch)
	h.mu.Unlock()
	close(ch)
}

func (h *localEventHub) publish(ev localEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subscribers {
		select {
		case ch <- ev:
		default:
		}
	}
}
