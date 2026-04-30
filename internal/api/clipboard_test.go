package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Cache-only tests don't need a real server; we exercise the cache
// directly to pin the TTL + cross-network-isolation invariants.

func TestClipboardCache_PutGet(t *testing.T) {
	c := &clipboardCache{entries: make(map[string]clipboardEntry)}
	now := time.Now()
	c.put(clipboardEntry{
		NetworkID: "net-a",
		NodeID:    "node-1",
		ClipID:    "clip-1",
		Hash:      "deadbeef",
		Type:      "text",
		Content:   []byte("hello"),
		CreatedAt: now,
	})
	got, ok := c.get("clip-1", "net-a", now)
	if !ok {
		t.Fatalf("get(clip-1) returned ok=false")
	}
	if string(got.Content) != "hello" {
		t.Errorf("Content: got %q", got.Content)
	}
}

func TestClipboardCache_TTLEvicts(t *testing.T) {
	// Tripwire: clips expire after 120s. After expiry, get() must
	// return ok=false even with a valid clipId — agents that cached
	// the clipId locally cannot still pull stale content from the
	// server. Bounded retention is a privacy invariant.
	c := &clipboardCache{entries: make(map[string]clipboardEntry)}
	now := time.Now()
	c.put(clipboardEntry{
		NetworkID: "net-a", ClipID: "clip-1",
		Content: []byte("old"), CreatedAt: now,
	})
	if _, ok := c.get("clip-1", "net-a", now.Add(60*time.Second)); !ok {
		t.Errorf("get within TTL window returned ok=false")
	}
	if _, ok := c.get("clip-1", "net-a", now.Add(121*time.Second)); ok {
		t.Errorf("get past TTL window returned ok=true (must expire)")
	}
}

func TestClipboardCache_CrossNetworkIsolation(t *testing.T) {
	// Tripwire (security-critical): a node in net-b MUST NOT be able
	// to pull a clip originated in net-a, even if it knows/guesses
	// the clipId. The cache enforces network scoping in get().
	c := &clipboardCache{entries: make(map[string]clipboardEntry)}
	now := time.Now()
	c.put(clipboardEntry{
		NetworkID: "net-a", ClipID: "shared-uuid",
		Content: []byte("net-a secret"), CreatedAt: now,
	})
	if _, ok := c.get("shared-uuid", "net-b", now); ok {
		t.Fatal("cross-network access leaked: net-b retrieved net-a's clip")
	}
	// Same clipId in same network still works.
	if _, ok := c.get("shared-uuid", "net-a", now); !ok {
		t.Fatal("same-network access blocked")
	}
}

func TestClipboardCache_EvictExpired(t *testing.T) {
	c := &clipboardCache{entries: make(map[string]clipboardEntry)}
	now := time.Now()
	c.put(clipboardEntry{NetworkID: "n", ClipID: "fresh", CreatedAt: now})
	c.put(clipboardEntry{NetworkID: "n", ClipID: "old", CreatedAt: now.Add(-200 * time.Second)})
	c.evictExpired(now)
	if _, ok := c.entries["old"]; ok {
		t.Errorf("expired entry not evicted")
	}
	if _, ok := c.entries["fresh"]; !ok {
		t.Errorf("fresh entry incorrectly evicted")
	}
}

// HTTP-level tests for the announce endpoint require a NodeStore
// stand-in; we lean on the existing pattern in renew_test.go and use
// a real in-memory sqlite store + EventHub. For this initial slice
// we keep coverage focused on the cache + endpoint validation logic
// (size cap, missing fields, type filtering) — auth + happy-path are
// integration-test territory and exercised end-to-end by the agent
// rolling-out side once it ships.

func TestClipboardAnnounce_RejectsMissingFields(t *testing.T) {
	h := &ClipboardHandler{Cache: newClipboardCache()}
	body, _ := json.Marshal(announceRequest{
		// NodeID intentionally missing
		ClipID: "x", Hash: "y",
	})
	r := httptest.NewRequest(http.MethodPost, "/api/clipboard/announce", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer faketoken")
	w := httptest.NewRecorder()
	h.Announce(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing nodeId: got %d, want 400", w.Code)
	}
}

func TestClipboardAnnounce_RejectsNonText(t *testing.T) {
	h := &ClipboardHandler{Cache: newClipboardCache()}
	body, _ := json.Marshal(announceRequest{
		NodeID: "n", ClipID: "x", Hash: "y", Type: "image",
	})
	r := httptest.NewRequest(http.MethodPost, "/api/clipboard/announce", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer faketoken")
	w := httptest.NewRecorder()
	h.Announce(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("non-text type: got %d, want 400", w.Code)
	}
}

func TestClipboardAnnounce_RejectsNoBearer(t *testing.T) {
	h := &ClipboardHandler{Cache: newClipboardCache()}
	body, _ := json.Marshal(announceRequest{
		NodeID: "n", ClipID: "x", Hash: "y", Content: []byte("hi"),
	})
	r := httptest.NewRequest(http.MethodPost, "/api/clipboard/announce", bytes.NewReader(body))
	// no Authorization header
	w := httptest.NewRecorder()
	h.Announce(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no bearer: got %d, want 401", w.Code)
	}
}

// Tripwire: 256 KB cap. We don't enforce here via JSON-decoder size
// (MaxBytesReader does that, but it's a stream limit and JSON
// base64 expansion changes the math). This test ensures the explicit
// content-length check past decode is in place.
func TestClipboardAnnounce_RejectsOversizeContent(t *testing.T) {
	h := &ClipboardHandler{Cache: newClipboardCache()}
	huge := bytes.Repeat([]byte("a"), 300*1024) // 300 KB > 256 KB cap
	body, _ := json.Marshal(announceRequest{
		NodeID: "n", ClipID: "x", Hash: "y", Type: "text", Content: huge,
	})
	r := httptest.NewRequest(http.MethodPost, "/api/clipboard/announce", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer faketoken")
	w := httptest.NewRecorder()
	h.Announce(w, r)
	// Accept either 413 (we cap explicitly) or 400 (MaxBytesReader
	// truncates and the JSON decoder fails). Both are correct
	// failure modes; the contract is "do not cache".
	if w.Code != http.StatusRequestEntityTooLarge && w.Code != http.StatusBadRequest {
		t.Errorf("oversize content: got %d, want 413 or 400", w.Code)
	}
	if len(h.Cache.entries) != 0 {
		t.Errorf("oversize content was cached anyway (count=%d)", len(h.Cache.entries))
	}
}
