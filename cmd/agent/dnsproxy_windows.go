//go:build windows

package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// Windows NRPT can't represent a DNS server on a non-standard port —
// the Add-DnsClientNrptRule cmdlet silently strips the :port suffix.
// Our lighthouse DNS listens on :15300, so the NRPT-configured queries
// go to :53 and time out (see CLAUDE.md Discovery Log / Windows Platform).
//
// Fix: run a small DNS forwarder on a loopback address, listening on
// the standard port 53. NRPT registers the loopback IP (no port field
// needed). Windows DNS client queries the loopback on :53 → we forward
// to the real upstream over its actual non-standard port.
//
// 127.53.0.0/24 is chosen to avoid collisions with systemd-resolved's
// 127.0.0.53 convention on Linux, and with anything else a Windows
// user might have on 127.0.0.1:53 (rare, but possible if they run a
// local DNS server). One instance per IP, allocated sequentially.

type windowsDNSProxy struct {
	upstream string
	loopback string // "127.53.0.N"
	udp      *dns.Server
	tcp      *dns.Server
}

var (
	dnsProxyMu      sync.Mutex
	activeDNSProxies = make(map[string]*windowsDNSProxy) // keyed by instance name
)

// allocateWindowsDNSLoopback returns a loopback IP in 127.53.0.0/24 for
// the given instance. Deterministic per-instance: reuses the IP of any
// existing proxy for this name, otherwise picks the next free slot.
// Caller holds dnsProxyMu.
func allocateWindowsDNSLoopbackLocked(instanceName string) (string, error) {
	if existing, ok := activeDNSProxies[instanceName]; ok {
		return existing.loopback, nil
	}
	used := make(map[string]bool, len(activeDNSProxies))
	for _, p := range activeDNSProxies {
		used[p.loopback] = true
	}
	for i := 1; i < 255; i++ {
		candidate := fmt.Sprintf("127.53.0.%d", i)
		if !used[candidate] {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("exhausted 127.53.0.0/24 loopback pool (254 instances?)")
}

// startWindowsDNSProxy launches a local DNS forwarder on a loopback
// address allocated for instanceName. Returns the bound loopback IP so
// the caller can register it in NRPT. Idempotent — calling with the
// same instance + upstream is a no-op; calling with a different
// upstream rebinds the existing proxy.
func startWindowsDNSProxy(instanceName, upstream string) (string, error) {
	dnsProxyMu.Lock()
	defer dnsProxyMu.Unlock()

	if existing, ok := activeDNSProxies[instanceName]; ok {
		if existing.upstream == upstream {
			return existing.loopback, nil
		}
		existing.shutdown()
		delete(activeDNSProxies, instanceName)
	}

	loopback, err := allocateWindowsDNSLoopbackLocked(instanceName)
	if err != nil {
		return "", err
	}
	addr := loopback + ":53"
	p := &windowsDNSProxy{upstream: upstream, loopback: loopback}

	// UDP is the common path; TCP is needed for large responses (EDNS fallback).
	p.udp = &dns.Server{
		Addr:         addr,
		Net:          "udp",
		Handler:      dns.HandlerFunc(p.handle),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	p.tcp = &dns.Server{
		Addr:         addr,
		Net:          "tcp",
		Handler:      dns.HandlerFunc(p.handle),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	// Both listeners must bind before we consider the proxy up. Spawn each,
	// then wait ~500ms; if either errors out immediately, bail.
	udpErr := make(chan error, 1)
	tcpErr := make(chan error, 1)

	go func() {
		if err := p.udp.ListenAndServe(); err != nil {
			udpErr <- err
		}
	}()
	go func() {
		if err := p.tcp.ListenAndServe(); err != nil {
			tcpErr <- err
		}
	}()

	select {
	case err := <-udpErr:
		return "", fmt.Errorf("dns proxy UDP bind %s: %w", addr, err)
	case err := <-tcpErr:
		return "", fmt.Errorf("dns proxy TCP bind %s: %w", addr, err)
	case <-time.After(500 * time.Millisecond):
	}

	activeDNSProxies[instanceName] = p
	log.Printf("[dns-proxy %s] listening on %s (udp+tcp) → upstream %s", instanceName, addr, upstream)
	return loopback, nil
}

// stopWindowsDNSProxy shuts down the proxy for one instance.
func stopWindowsDNSProxy(instanceName string) {
	dnsProxyMu.Lock()
	defer dnsProxyMu.Unlock()
	p, ok := activeDNSProxies[instanceName]
	if !ok {
		return
	}
	p.shutdown()
	delete(activeDNSProxies, instanceName)
	log.Printf("[dns-proxy %s] stopped", instanceName)
}

func (p *windowsDNSProxy) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if p.udp != nil {
		p.udp.ShutdownContext(ctx)
	}
	if p.tcp != nil {
		p.tcp.ShutdownContext(ctx)
	}
}

// handle forwards the incoming query to the upstream and writes the
// response back to the client. On upstream error, returns SERVFAIL so
// the client sees a failure quickly rather than waiting for its own
// query timeout.
func (p *windowsDNSProxy) handle(w dns.ResponseWriter, r *dns.Msg) {
	c := new(dns.Client)
	c.Timeout = 3 * time.Second
	resp, _, err := c.Exchange(r, p.upstream)
	if err != nil {
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeServerFailure)
		_ = w.WriteMsg(m)
		return
	}
	_ = w.WriteMsg(resp)
}
