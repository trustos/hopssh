package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// serverSet tracks the HTTP servers serving the agent API — one per
// mesh instance (bound to that mesh's Nebula listener) plus an
// optional unscoped OS-stack listener for enrollment-less debug runs.
//
// Each instance entry owns its own *http.Server so cert-renewal
// restarts (which shut down and replace the listener) don't affect
// other instances.
type serverSet struct {
	mu      sync.Mutex
	servers map[string]*instanceServer // keyed by enrollment name
	bare    *http.Server               // un-scoped debug listener (rare)
}

// instanceServer holds per-instance HTTP state. mu protects srv for
// replace-on-Nebula-restart.
type instanceServer struct {
	mu  sync.Mutex
	srv *http.Server
}

func newServerSet() *serverSet {
	return &serverSet{servers: make(map[string]*instanceServer)}
}

// newHTTPServer builds a *http.Server matching the historical settings:
// streaming-friendly WriteTimeout (0) for /exec and /shell, short
// ReadHeaderTimeout to block Slowloris attacks.
func newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      0,
	}
}

// startMeshListener starts an instance-scoped HTTP server bound to the
// given mesh service's listener. Blocking errors log + exit.
func (s *serverSet) startMeshListener(inst *meshInstance, handler http.Handler, svc meshService, address string) error {
	ln, err := svc.Listen("tcp", address)
	if err != nil {
		return err
	}
	srv := newHTTPServer(handler)
	s.mu.Lock()
	s.servers[inst.name()] = &instanceServer{srv: srv}
	s.mu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[agent %s] Serve: %v", inst.name(), err)
		}
	}()
	return nil
}

// startOSListener starts an instance-scoped HTTP server on the OS
// network stack. Used only when Nebula fails and we want the renewal
// loop to still reach the control plane. Soft-fail on bind error (a
// port collision with another instance's OS-stack fallback shouldn't
// kill the whole agent — the renewal loop for this instance will
// still run and can recover Nebula).
func (s *serverSet) startOSListener(inst *meshInstance, handler http.Handler, address string) {
	ln, err := net.Listen("tcp", address)
	if err != nil {
		log.Printf("[agent %s] OS-stack fallback listen %s: %v (renewal loop still active)", inst.name(), address, err)
		return
	}
	log.Printf("[agent %s] listening on %s (OS stack)", inst.name(), ln.Addr())
	srv := newHTTPServer(handler)
	s.mu.Lock()
	s.servers[inst.name()] = &instanceServer{srv: srv}
	s.mu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[agent %s] Serve: %v", inst.name(), err)
		}
	}()
}

// startUnscopedOSListener starts a single OS-stack server not tied to
// any instance. Used when the agent has zero enrollments.
func (s *serverSet) startUnscopedOSListener(handler http.Handler, address string) error {
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen %s: %w", address, err)
	}
	log.Printf("hop-agent listening on %s (OS stack, no enrollment)", ln.Addr())
	srv := newHTTPServer(handler)
	s.mu.Lock()
	s.bare = srv
	s.mu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Serve: %v", err)
		}
	}()
	return nil
}

// rebindMesh shuts down the instance's current HTTP server and starts
// a new one bound to a freshly-restarted Nebula listener. Invoked from
// the onRestart callback in reloadNebula.
func (s *serverSet) rebindMesh(inst *meshInstance, handler http.Handler, svc meshService) error {
	s.mu.Lock()
	entry := s.servers[inst.name()]
	s.mu.Unlock()
	if entry == nil {
		// No prior server → fall through to a fresh startMeshListener.
		return s.startMeshListener(inst, handler, svc, fmt.Sprintf(":%d", agentAPIPort))
	}
	entry.mu.Lock()
	oldSrv := entry.srv
	entry.mu.Unlock()

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = oldSrv.Shutdown(shutCtx)
	cancel()

	newLn, err := svc.Listen("tcp", fmt.Sprintf(":%d", agentAPIPort))
	if err != nil {
		return err
	}
	newSrv := newHTTPServer(handler)
	entry.mu.Lock()
	entry.srv = newSrv
	entry.mu.Unlock()
	go func() {
		if err := newSrv.Serve(newLn); err != nil && err != http.ErrServerClosed {
			log.Printf("[agent %s] Serve after Nebula restart: %v", inst.name(), err)
		}
	}()
	log.Printf("[agent %s] HTTP server restarted on new Nebula mesh listener", inst.name())
	return nil
}

// shutdownAll stops every tracked HTTP server gracefully.
func (s *serverSet) shutdownAll() {
	s.mu.Lock()
	entries := make([]*instanceServer, 0, len(s.servers))
	for _, e := range s.servers {
		entries = append(entries, e)
	}
	s.servers = make(map[string]*instanceServer)
	bare := s.bare
	s.bare = nil
	s.mu.Unlock()

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, e := range entries {
		e.mu.Lock()
		srv := e.srv
		e.srv = nil
		e.mu.Unlock()
		if srv != nil {
			_ = srv.Shutdown(shutCtx)
		}
	}
	if bare != nil {
		_ = bare.Shutdown(shutCtx)
	}
}
