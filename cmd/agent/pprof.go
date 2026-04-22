package main

import (
	"log"
	"net"
	"net/http"
	netpprof "net/http/pprof"
	"os"
	"strings"
	"time"
)

// startPprofIfRequested optionally starts a loopback-only HTTP server
// that exposes Go's net/http/pprof endpoints. Activated by setting the
// environment variable HOPSSH_PPROF_ADDR to a host:port (e.g.
// "127.0.0.1:6060"). The address MUST be a loopback address — we
// reject anything else at startup so an operator can't accidentally
// expose unauthenticated profiling data on the LAN or mesh.
//
// The agent's main mesh-side mux already exposes pprof, but it sits
// behind the per-enrollment bearer token. For local development —
// especially the macOS RTT-latency investigation in
// docs/macos-latency-research.md — pulling that token out of disk and
// passing it to `go tool pprof` is friction. A 127.0.0.1-bound
// listener with no auth is the standard pattern; the loopback bind is
// the security boundary.
//
// Endpoints exposed (paths match net/http/pprof defaults):
//
//	/debug/pprof/         (index)
//	/debug/pprof/profile  (30 s CPU profile by default)
//	/debug/pprof/heap     (heap profile)
//	/debug/pprof/goroutine
//	/debug/pprof/trace    (execution tracer)
//	/debug/pprof/cmdline
//	/debug/pprof/symbol
//
// Typical use:
//
//	HOPSSH_PPROF_ADDR=127.0.0.1:6060 hop-agent serve ...
//	go tool pprof -seconds 30 http://127.0.0.1:6060/debug/pprof/profile
func startPprofIfRequested() {
	addr := strings.TrimSpace(os.Getenv("HOPSSH_PPROF_ADDR"))
	if addr == "" {
		return
	}

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		log.Printf("[agent] pprof: HOPSSH_PPROF_ADDR=%q is not host:port: %v — pprof disabled", addr, err)
		return
	}
	if !isLoopbackHost(host) {
		log.Printf("[agent] pprof: HOPSSH_PPROF_ADDR=%q is not loopback — pprof disabled (use 127.0.0.1 or ::1)", addr)
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", netpprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", netpprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", netpprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", netpprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", netpprof.Trace)
	// netpprof.Index serves /heap, /goroutine, /allocs, /block, /mutex,
	// /threadcreate via the same handler — the prefix match above
	// already routes them.

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("[agent] pprof: listen %s failed: %v — pprof disabled", addr, err)
		return
	}

	log.Printf("[agent] pprof: serving on http://%s/debug/pprof/ (loopback only, no auth)", addr)
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[agent] pprof: server exited: %v", err)
		}
	}()
}

// isLoopbackHost reports whether host resolves to a loopback address
// without going through DNS — we accept the literal forms only so an
// operator can't sneak a LAN-routable name past us. Bare "localhost"
// is allowed because some platforms map it via /etc/hosts to 127.0.0.1
// at startup; net.ParseIP returns nil for it so we whitelist by name.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}
