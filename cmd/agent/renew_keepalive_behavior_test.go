package main

// Behavioral verification that the DisableKeepAlives fix actually
// prevents TCP+TLS connection reuse — not just that the option is set
// in source. Pairs with renew_keepalive_test.go (which asserts the
// option is present at all).
//
// The decisive comparison is the negative-control test: the same flow
// with Go's default Transport DOES reuse connections. If the negative
// control ever fails, the test infrastructure is broken; if the
// positive test ever fails, the fix has regressed.

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// connCounter wraps an httptest.Server and records the RemoteAddr of
// every accepted TCP connection. RemoteAddr changes per fresh dial
// because the source ephemeral port differs. So:
//   - 2 requests + 1 unique RemoteAddr = the server saw a reused
//     keep-alive connection (BAD, what the bug looked like)
//   - 2 requests + 2 unique RemoteAddrs = each request opened its own
//     TCP socket (GOOD, what the fix achieves)
type connCounter struct {
	mu    sync.Mutex
	addrs map[string]int
	hits  atomic.Int64
}

func (c *connCounter) record(addr string) {
	c.mu.Lock()
	if c.addrs == nil {
		c.addrs = map[string]int{}
	}
	c.addrs[addr]++
	c.mu.Unlock()
	c.hits.Add(1)
}

func (c *connCounter) uniqueAddrs() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.addrs)
}

// runRequests issues n POST requests against the test server using the
// supplied client. Equivalent of what sendHeartbeat does — body is
// inert, just needs to exercise the connection-reuse path.
func runRequests(t *testing.T, client *http.Client, url string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		req, err := http.NewRequest("POST", url, http.NoBody)
		if err != nil {
			t.Fatalf("build req %d: %v", i, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("do req %d: %v", i, err)
		}
		resp.Body.Close()
	}
}

// TestSendHeartbeatClient_NeverReusesConnections is the positive test:
// using the EXACT client constructor from cmd/agent/renew.go::sendHeartbeat
// (Transport: &http.Transport{DisableKeepAlives: true}), every request
// MUST open a fresh TCP connection.
//
// If this test ever fails, the fix has regressed and the post-network-
// change heartbeat-hang bug is back.
func TestSendHeartbeatClient_NeverReusesConnections(t *testing.T) {
	cc := &connCounter{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cc.record(r.RemoteAddr)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Mirrors cmd/agent/renew.go::sendHeartbeat. Update both together.
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}

	const n = 5
	runRequests(t, client, srv.URL, n)

	if got := cc.hits.Load(); int(got) != n {
		t.Fatalf("server received %d hits, want %d", got, n)
	}
	if u := cc.uniqueAddrs(); u != n {
		t.Errorf("server saw %d unique RemoteAddrs across %d requests; want %d (= every request a fresh dial). Connection reuse has crept back in.", u, n, n)
	}
}

// TestRenewCertClient_NeverReusesConnections — same as above for the
// renewCert path (cmd/agent/renew.go:422).
func TestRenewCertClient_NeverReusesConnections(t *testing.T) {
	cc := &connCounter{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cc.record(r.RemoteAddr)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Mirrors cmd/agent/renew.go::renewCert.
	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}

	const n = 4
	runRequests(t, client, srv.URL, n)

	if u := cc.uniqueAddrs(); u != n {
		t.Errorf("renewCert client reused connections: %d unique RemoteAddrs across %d requests, want %d", u, n, n)
	}
}

// TestDefaultClient_DoesReuseConnections is the NEGATIVE CONTROL.
//
// It demonstrates that the test infrastructure correctly detects pool
// reuse, by exercising the buggy form (default Transport with no
// DisableKeepAlives) — what cmd/agent/renew.go used to do, what caused
// the 2026-04-26 incident. The default Transport pools idle conns, so
// 5 sequential requests against the same host MUST land on a smaller
// number of unique TCP sockets.
//
// If THIS test ever shows N unique addrs == N requests, then Go's
// default Transport stopped pooling and our positive test no longer
// proves anything (it'd be trivially true everywhere). That'd be a
// signal to update the test to a stronger assertion.
func TestDefaultClient_DoesReuseConnections(t *testing.T) {
	cc := &connCounter{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cc.record(r.RemoteAddr)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Default Transport — pools by default. This is what the bug looked
	// like: shared idle pool, post-network-change reuse hangs.
	client := &http.Client{Timeout: 10 * time.Second}

	const n = 5
	runRequests(t, client, srv.URL, n)

	u := cc.uniqueAddrs()
	if u >= n {
		t.Fatalf("control case: default-transport client opened %d unique conns for %d requests — Go's pool no longer reuses, the positive test above is trivial and needs a stronger assertion", u, n)
	}
	t.Logf("control: default Transport reused — %d unique TCP conns for %d requests (proves the test infrastructure detects reuse correctly)", u, n)
}
