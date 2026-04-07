package api

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/trustos/hopssh/internal/auth"
	"github.com/trustos/hopssh/internal/authz"
	"github.com/trustos/hopssh/internal/db"
)

// Event is a real-time notification pushed to dashboard clients.
type Event struct {
	Type string      `json:"type"` // e.g. "node.enrolled", "node.status", "dns.changed", "member.changed"
	Data interface{} `json:"data,omitempty"`
}

// EventHub manages per-network WebSocket subscribers for real-time dashboard updates.
type EventHub struct {
	mu          sync.RWMutex
	subscribers map[string]map[chan Event]struct{} // networkID → set of channels
}

func NewEventHub() *EventHub {
	return &EventHub{
		subscribers: make(map[string]map[chan Event]struct{}),
	}
}

// Subscribe returns a channel that receives events for a network.
func (h *EventHub) Subscribe(networkID string) chan Event {
	h.mu.Lock()
	defer h.mu.Unlock()

	ch := make(chan Event, 16)
	if h.subscribers[networkID] == nil {
		h.subscribers[networkID] = make(map[chan Event]struct{})
	}
	h.subscribers[networkID][ch] = struct{}{}
	return ch
}

// Unsubscribe removes a subscriber channel.
func (h *EventHub) Unsubscribe(networkID string, ch chan Event) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if subs, ok := h.subscribers[networkID]; ok {
		delete(subs, ch)
		if len(subs) == 0 {
			delete(h.subscribers, networkID)
		}
	}
	close(ch)
}

// Publish sends an event to all subscribers of a network.
func (h *EventHub) Publish(networkID string, event Event) {
	h.mu.RLock()
	subs := h.subscribers[networkID]
	h.mu.RUnlock()

	for ch := range subs {
		select {
		case ch <- event:
		default:
			// Drop if subscriber is slow — they'll get the next event.
		}
	}
}

// EventsHandler serves the WebSocket endpoint for real-time dashboard updates.
type EventsHandler struct {
	Networks *db.NetworkStore
	Members  *db.NetworkMemberStore
	Hub      *EventHub
}

var eventsUpgrader = websocket.Upgrader{
	CheckOrigin: checkWebSocketOrigin,
}

func checkWebSocketOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	host := r.Host
	return origin == "http://"+host || origin == "https://"+host ||
		containsOrigin(AllowedOrigins, origin)
}

func containsOrigin(origins []string, origin string) bool {
	for _, o := range origins {
		if o == "*" || o == origin {
			return true
		}
	}
	return false
}

// Connect upgrades to a WebSocket and streams events for a network.
// GET /api/networks/{networkID}/events
func (h *EventsHandler) Connect(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")

	network, err := h.Networks.Get(networkID)
	if err != nil || network == nil {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}

	var membership *db.NetworkMember
	if h.Members != nil {
		membership, _ = h.Members.GetMembership(networkID, user.ID)
	}
	access := authz.CheckAccess(user, network, membership)
	if !access.CanView() {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}

	conn, err := eventsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[events] WebSocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	ch := h.Hub.Subscribe(networkID)
	defer h.Hub.Unsubscribe(networkID, ch)

	// Read pump: just drain pings/close frames from the client.
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	// Write pump: send events to client.
	for event := range ch {
		data, err := json.Marshal(event)
		if err != nil {
			continue
		}
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			return
		}
	}
}
