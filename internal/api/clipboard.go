package api

// Server-side clipboard relay (Phase L1, slice 1).
//
// Agents POST clipboard announcements to /api/clipboard/announce; the
// server caches per-network with TTL and emits a clipboard.announce
// event on the network's event hub. Other agents (subscribed to the
// hub via /api/networks/{id}/events) react by either applying the
// inline content directly or pulling /api/clipboard/{clipId} for
// larger payloads.
//
// Privacy + safety:
//   - 256 KB hard cap on advertised content
//   - 120 s TTL: agent can no longer fetch a clip after that window
//   - In-memory only — never persisted to disk
//   - Per-network opt-in is enforced agent-side (each agent decides
//     whether to publish/apply); server happily relays anything for
//     authenticated nodes. This keeps the server out of the trust
//     boundary for the user-facing toggle.
//   - Concealed-pasteboard items (1Password "concealed" UTI) are
//     filtered agent-side BEFORE the announce POST — server never
//     sees them.

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/trustos/hopssh/internal/db"
)

// Maximum size of clipboard content the server will relay. Larger
// clips return 413 Payload Too Large. Tuned to cover 99% of plain
// text (URLs, code snippets, configs) without burning bandwidth on
// runaway large copies (hex dumps, base64 blobs, etc.). Larger
// payloads are out of scope for the v1 text-only release.
const clipboardMaxBytes = 256 * 1024 // 256 KB

// Time-to-live for cached clips. After this window the agent's GET
// for the clip ID returns 404 — clipboard content is ephemeral.
// Matches Apple Universal Clipboard's 120s TTL.
const clipboardTTL = 120 * time.Second

// clipboardCache is the in-memory store keyed by clip ID. Entries
// expire automatically via the cleanup goroutine. A separate
// sync.Map per server instance — never persisted, never shared.
type clipboardCache struct {
	mu      sync.RWMutex
	entries map[string]clipboardEntry
}

type clipboardEntry struct {
	NetworkID string
	NodeID    string // originator
	ClipID    string
	Hash      string
	Type      string // "text"
	Content   []byte
	CreatedAt time.Time
}

func newClipboardCache() *clipboardCache {
	c := &clipboardCache{entries: make(map[string]clipboardEntry)}
	// Sweep expired entries every 30s. Cheap — handful of entries
	// per active network at most.
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			c.evictExpired(time.Now())
		}
	}()
	return c
}

func (c *clipboardCache) evictExpired(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, e := range c.entries {
		if now.Sub(e.CreatedAt) > clipboardTTL {
			delete(c.entries, id)
		}
	}
}

func (c *clipboardCache) put(e clipboardEntry) {
	c.mu.Lock()
	c.entries[e.ClipID] = e
	c.mu.Unlock()
}

// get returns the cached entry for clipId IF the requesting node is
// in the same network as the originator. Returns ok=false on miss,
// expiry, or cross-network access.
func (c *clipboardCache) get(clipID, requestingNetworkID string, now time.Time) (clipboardEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[clipID]
	if !ok {
		return clipboardEntry{}, false
	}
	if now.Sub(e.CreatedAt) > clipboardTTL {
		return clipboardEntry{}, false
	}
	if e.NetworkID != requestingNetworkID {
		return clipboardEntry{}, false
	}
	return e, true
}

// ClipboardHandler relays clipboard announcements between agents in
// the same network via the existing per-network event hub.
type ClipboardHandler struct {
	Nodes    *db.NodeStore
	EventHub *EventHub
	Cache    *clipboardCache
}

// NewClipboardHandler constructs a handler with a fresh in-memory
// cache. One per server process; the sweep goroutine starts here.
func NewClipboardHandler(nodes *db.NodeStore, hub *EventHub) *ClipboardHandler {
	return &ClipboardHandler{
		Nodes:    nodes,
		EventHub: hub,
		Cache:    newClipboardCache(),
	}
}

// announceRequest is the body the agent POSTs.
type announceRequest struct {
	NodeID  string `json:"nodeId"`
	ClipID  string `json:"clipId"`
	Hash    string `json:"hash"`
	Type    string `json:"type"`
	Content []byte `json:"content,omitempty"` // base64 in JSON; absent for metadata-only announces
	Size    int    `json:"size,omitempty"`    // declared size; sanity-check against actual
}

// Announce accepts a clipboard announcement from an agent.
// POST /api/clipboard/announce — bearer-token auth (same model as
// /api/heartbeat: the agent passes its node-specific token).
func (h *ClipboardHandler) Announce(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, clipboardMaxBytes+8*1024) // payload + header overhead

	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")

	var body announceRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	if body.NodeID == "" || body.ClipID == "" || body.Hash == "" {
		http.Error(w, "missing required field", http.StatusBadRequest)
		return
	}
	if body.Type == "" {
		body.Type = "text"
	}
	if body.Type != "text" {
		http.Error(w, "only type=text supported in v1", http.StatusBadRequest)
		return
	}
	if len(body.Content) > clipboardMaxBytes {
		http.Error(w, "content too large", http.StatusRequestEntityTooLarge)
		return
	}

	// Auth: load node, verify token via constant-time compare.
	node, err := h.Nodes.Get(body.NodeID)
	if err != nil || node == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(node.AgentToken)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Cache the entry. Subsequent GET /api/clipboard/{clipId} from
	// peers in the same network can fetch full content.
	h.Cache.put(clipboardEntry{
		NetworkID: node.NetworkID,
		NodeID:    body.NodeID,
		ClipID:    body.ClipID,
		Hash:      body.Hash,
		Type:      body.Type,
		Content:   body.Content,
		CreatedAt: time.Now(),
	})

	// Emit via the existing event hub; subscribed agents react in
	// near-real-time. Payload carries metadata + small content
	// inline (≤8KB) so peers that opt to apply directly don't need
	// a follow-up GET. Larger content is fetched via /api/clipboard/{clipId}.
	const inlineThreshold = 8 * 1024
	eventData := map[string]any{
		"clipId":     body.ClipID,
		"hash":       body.Hash,
		"type":       body.Type,
		"size":       len(body.Content),
		"originator": body.NodeID,
	}
	if len(body.Content) > 0 && len(body.Content) <= inlineThreshold {
		eventData["content"] = body.Content
	}
	if h.EventHub != nil {
		h.EventHub.Publish(node.NetworkID, Event{
			Type: "clipboard.announce",
			Data: eventData,
		})
	}

	w.WriteHeader(http.StatusNoContent)
}

// latestForNetwork returns the most-recently-cached entry for the
// requesting network, EXCLUDING ones originated by the requesting
// node. Used by the agent's poll-on-paste path.
func (c *clipboardCache) latestForNetwork(networkID, excludeNodeID string, now time.Time) (clipboardEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var best clipboardEntry
	found := false
	for _, e := range c.entries {
		if e.NetworkID != networkID {
			continue
		}
		if e.NodeID == excludeNodeID {
			continue
		}
		if now.Sub(e.CreatedAt) > clipboardTTL {
			continue
		}
		if !found || e.CreatedAt.After(best.CreatedAt) {
			best = e
			found = true
		}
	}
	return best, found
}

// Poll is the short-poll endpoint agents call to fetch the most
// recent clipboard announcement for their network from another peer.
// GET /api/clipboard/poll?nodeId=<id>&since=<unix-ts> — returns the
// latest entry NEWER than `since` excluding own announces. Returns
// 204 No Content if nothing newer.
//
// Cadence on the agent side: every 2 s while clipboard sync is
// enabled. The combined 2 s poll + 120 s TTL window means clips
// linger long enough for late-joining peers but bounded enough for
// privacy.
func (h *ClipboardHandler) Poll(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")

	requestingNodeID := r.URL.Query().Get("nodeId")
	if requestingNodeID == "" {
		http.Error(w, "nodeId required", http.StatusBadRequest)
		return
	}
	requestingNode, err := h.Nodes.Get(requestingNodeID)
	if err != nil || requestingNode == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(requestingNode.AgentToken)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// since: unix seconds. 0 → return the latest regardless of age
	// (subject to TTL). The agent will dedup by clipId on its side.
	var since int64
	if s := r.URL.Query().Get("since"); s != "" {
		// Parse manually to avoid pulling in strconv elsewhere.
		// Numeric only; non-numeric → since = 0.
		var n int64
		for i := 0; i < len(s); i++ {
			d := s[i]
			if d < '0' || d > '9' {
				n = 0
				break
			}
			n = n*10 + int64(d-'0')
		}
		since = n
	}

	entry, ok := h.Cache.latestForNetwork(requestingNode.NetworkID, requestingNodeID, time.Now())
	if !ok || entry.CreatedAt.Unix() <= since {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"clipId":     entry.ClipID,
		"hash":       entry.Hash,
		"type":       entry.Type,
		"originator": entry.NodeID,
		"ts":         entry.CreatedAt.Unix(),
		"content":    entry.Content,
	})
}

// Content serves the cached payload for a specific clipId. Used by
// peers that received an announce without inline content (size > 8KB).
// GET /api/clipboard/{clipId} — bearer-token auth, cross-network
// access blocked by the cache.
func (h *ClipboardHandler) Content(w http.ResponseWriter, r *http.Request) {
	clipID := chi.URLParam(r, "clipId")
	if clipID == "" {
		http.Error(w, "clipId required", http.StatusBadRequest)
		return
	}

	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")

	// The requesting agent passes its own nodeId in a query param so
	// we can scope the cache lookup. (Bearer alone doesn't tell us
	// which network — same token format across networks.)
	requestingNodeID := r.URL.Query().Get("nodeId")
	if requestingNodeID == "" {
		http.Error(w, "nodeId query param required", http.StatusBadRequest)
		return
	}
	requestingNode, err := h.Nodes.Get(requestingNodeID)
	if err != nil || requestingNode == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(requestingNode.AgentToken)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	entry, ok := h.Cache.get(clipID, requestingNode.NetworkID, time.Now())
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"clipId":  entry.ClipID,
		"hash":    entry.Hash,
		"type":    entry.Type,
		"content": entry.Content,
	})
}
