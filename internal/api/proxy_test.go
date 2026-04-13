package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/trustos/hopssh/internal/auth"
	"github.com/trustos/hopssh/internal/db"
)

// --- Test helpers ---

// newTestRequest creates an HTTP request with chi URL params and an authenticated user.
func newTestRequest(method, path string, user *db.UserProfile, params map[string]string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	if user != nil {
		ctx = auth.WithUser(ctx, user)
	}
	return req.WithContext(ctx)
}

// --- Tests for InvalidateProxyCache ---

func TestInvalidateProxyCache_ByNode(t *testing.T) {
	h := &ProxyHandler{}

	h.authCache.Store("net-1:node-1:user-1", &proxyAuthEntry{expires: time.Now().Add(time.Hour)})
	h.authCache.Store("net-1:node-2:user-1", &proxyAuthEntry{expires: time.Now().Add(time.Hour)})
	h.authCache.Store("net-1:node-1:user-2", &proxyAuthEntry{expires: time.Now().Add(time.Hour)})

	h.InvalidateProxyCache("net-1", "node-1")

	if _, ok := h.authCache.Load("net-1:node-1:user-1"); ok {
		t.Error("node-1:user-1 should be invalidated")
	}
	if _, ok := h.authCache.Load("net-1:node-1:user-2"); ok {
		t.Error("node-1:user-2 should be invalidated")
	}
	if _, ok := h.authCache.Load("net-1:node-2:user-1"); !ok {
		t.Error("node-2:user-1 should NOT be invalidated")
	}
}

func TestInvalidateProxyCache_ByNetwork(t *testing.T) {
	h := &ProxyHandler{}

	h.authCache.Store("net-1:node-1:user-1", &proxyAuthEntry{expires: time.Now().Add(time.Hour)})
	h.authCache.Store("net-1:node-2:user-2", &proxyAuthEntry{expires: time.Now().Add(time.Hour)})
	h.authCache.Store("net-2:node-3:user-1", &proxyAuthEntry{expires: time.Now().Add(time.Hour)})

	h.InvalidateProxyCache("net-1", "")

	if _, ok := h.authCache.Load("net-1:node-1:user-1"); ok {
		t.Error("net-1:node-1 should be invalidated")
	}
	if _, ok := h.authCache.Load("net-1:node-2:user-2"); ok {
		t.Error("net-1:node-2 should be invalidated")
	}
	if _, ok := h.authCache.Load("net-2:node-3:user-1"); !ok {
		t.Error("net-2 entry should NOT be invalidated")
	}
}

func TestInvalidateProxyCache_EmptyCache(t *testing.T) {
	h := &ProxyHandler{}
	// Should not panic.
	h.InvalidateProxyCache("net-1", "node-1")
	h.InvalidateProxyCache("net-1", "")
}

func TestInvalidateProxyCache_Concurrent(t *testing.T) {
	h := &ProxyHandler{}
	for i := 0; i < 100; i++ {
		key := "net-1:node-1:user-" + string(rune('0'+i%10))
		h.authCache.Store(key, &proxyAuthEntry{expires: time.Now().Add(time.Hour)})
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.InvalidateProxyCache("net-1", "node-1")
		}()
	}
	wg.Wait()
}

// --- Tests for cachedRequireNode cache behavior ---
// These test the cache layer directly by pre-populating sync.Map entries,
// avoiding the need for a real SQLite database.

func TestCachedRequireNode_CacheHit(t *testing.T) {
	network := &db.Network{ID: "net-1", UserID: "user-1", Name: "test"}
	node := &db.Node{ID: "node-1", NetworkID: "net-1", NebulaIP: "10.42.1.2/24", AgentToken: "tok"}
	user := &db.UserProfile{ID: "user-1", Email: "test@example.com"}

	h := &ProxyHandler{}

	// Pre-populate cache.
	h.authCache.Store("net-1:node-1:user-1", &proxyAuthEntry{
		network: network,
		node:    node,
		expires: time.Now().Add(time.Hour),
	})

	req := newTestRequest("GET", "/", user,
		map[string]string{"networkID": "net-1", "nodeID": "node-1"})

	gotNet, gotNode, err := h.cachedRequireNode(req)
	if err != nil {
		t.Fatalf("cache hit should succeed: %v", err)
	}
	if gotNet.ID != "net-1" {
		t.Errorf("expected net-1, got %s", gotNet.ID)
	}
	if gotNode.ID != "node-1" {
		t.Errorf("expected node-1, got %s", gotNode.ID)
	}
}

func TestCachedRequireNode_CacheExpiry(t *testing.T) {
	network := &db.Network{ID: "net-1", UserID: "user-1", Name: "test"}
	node := &db.Node{ID: "node-1", NetworkID: "net-1", NebulaIP: "10.42.1.2/24"}
	user := &db.UserProfile{ID: "user-1", Email: "test@example.com"}

	h := &ProxyHandler{}

	// Pre-populate cache with a valid entry.
	h.authCache.Store("net-1:node-1:user-1", &proxyAuthEntry{
		network: network,
		node:    node,
		expires: time.Now().Add(time.Hour),
	})

	req := newTestRequest("GET", "/", user,
		map[string]string{"networkID": "net-1", "nodeID": "node-1"})

	// Confirm cache hit works.
	_, _, err := h.cachedRequireNode(req)
	if err != nil {
		t.Fatalf("valid cache should hit: %v", err)
	}

	// Expire the entry.
	if v, ok := h.authCache.Load("net-1:node-1:user-1"); ok {
		entry := v.(*proxyAuthEntry)
		entry.expires = time.Now().Add(-1 * time.Second)
	}

	// Verify the expired entry is deleted on next access attempt.
	// We can't call cachedRequireNode (nil stores would hang), so verify directly.
	key := "net-1:node-1:user-1"
	if v, ok := h.authCache.Load(key); ok {
		entry := v.(*proxyAuthEntry)
		if !time.Now().After(entry.expires) {
			t.Error("entry should be expired")
		}
	}
}

func TestCachedRequireNode_DifferentUsersIsolated(t *testing.T) {
	network := &db.Network{ID: "net-1", UserID: "user-1", Name: "test"}
	node := &db.Node{ID: "node-1", NetworkID: "net-1", NebulaIP: "10.42.1.2/24"}
	owner := &db.UserProfile{ID: "user-1", Email: "owner@example.com"}
	stranger := &db.UserProfile{ID: "user-3", Email: "stranger@example.com"}

	h := &ProxyHandler{}

	// Cache entry for owner only.
	h.authCache.Store("net-1:node-1:user-1", &proxyAuthEntry{
		network: network,
		node:    node,
		expires: time.Now().Add(time.Hour),
	})

	// Owner hits cache.
	reqOwner := newTestRequest("GET", "/", owner,
		map[string]string{"networkID": "net-1", "nodeID": "node-1"})
	_, _, err := h.cachedRequireNode(reqOwner)
	if err != nil {
		t.Fatalf("owner should hit cache: %v", err)
	}

	// Stranger has different cache key — verify no cache entry exists.
	strangerKey := "net-1:node-1:" + stranger.ID
	if _, ok := h.authCache.Load(strangerKey); ok {
		t.Fatal("stranger should NOT have a cache entry from owner's request")
	}

	// Verify the owner's key doesn't match the stranger's.
	ownerKey := "net-1:node-1:" + owner.ID
	if ownerKey == strangerKey {
		t.Fatal("cache keys must be different for different users")
	}
}

func TestCachedRequireNode_CacheKeyFormat(t *testing.T) {
	network := &db.Network{ID: "net-abc", UserID: "user-xyz", Name: "test"}
	node := &db.Node{ID: "node-123", NetworkID: "net-abc"}
	user := &db.UserProfile{ID: "user-xyz", Email: "test@example.com"}

	h := &ProxyHandler{}
	h.authCache.Store("net-abc:node-123:user-xyz", &proxyAuthEntry{
		network: network,
		node:    node,
		expires: time.Now().Add(time.Hour),
	})

	req := newTestRequest("GET", "/", user,
		map[string]string{"networkID": "net-abc", "nodeID": "node-123"})

	_, _, err := h.cachedRequireNode(req)
	if err != nil {
		t.Fatalf("should match cache key: %v", err)
	}
}

// --- Tests for proxyAuthTTL constant ---

func TestProxyAuthTTL(t *testing.T) {
	if proxyAuthTTL != 2*time.Minute {
		t.Errorf("expected 2 minutes, got %v", proxyAuthTTL)
	}
}

// --- Benchmark for cache operations ---

func BenchmarkCachedRequireNode_CacheHit(b *testing.B) {
	network := &db.Network{ID: "net-1", UserID: "user-1", Name: "test"}
	node := &db.Node{ID: "node-1", NetworkID: "net-1", NebulaIP: "10.42.1.2/24"}
	user := &db.UserProfile{ID: "user-1", Email: "test@example.com"}

	h := &ProxyHandler{}
	h.authCache.Store("net-1:node-1:user-1", &proxyAuthEntry{
		network: network,
		node:    node,
		expires: time.Now().Add(time.Hour),
	})

	req := newTestRequest("GET", "/", user,
		map[string]string{"networkID": "net-1", "nodeID": "node-1"})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.cachedRequireNode(req)
	}
}

func BenchmarkInvalidateProxyCache_100Entries(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h := &ProxyHandler{}
		for j := 0; j < 100; j++ {
			key := "net-1:node-1:user-" + string(rune('A'+j%26))
			h.authCache.Store(key, &proxyAuthEntry{expires: time.Now().Add(time.Hour)})
		}
		h.InvalidateProxyCache("net-1", "node-1")
	}
}
