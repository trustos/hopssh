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
// 127.53.0.1 is chosen to avoid collisions with systemd-resolved's
// 127.0.0.53 convention on Linux, and with anything else a Windows
// user might have on 127.0.0.1:53 (rare, but possible if they run a
// local DNS server).
const windowsDNSProxyAddr = "127.53.0.1:53"

// WindowsDNSProxyIP is the loopback the proxy binds to. Used by NRPT
// registration. Kept as a package var for visibility in logs/tests.
var WindowsDNSProxyIP = "127.53.0.1"

type windowsDNSProxy struct {
	upstream string
	udp      *dns.Server
	tcp      *dns.Server
}

var (
	dnsProxyMu     sync.Mutex
	activeDNSProxy *windowsDNSProxy
)

// startWindowsDNSProxy launches a local DNS forwarder bound to
// windowsDNSProxyAddr that relays queries to the given upstream
// (e.g., "132.145.232.64:15300"). Idempotent — calling it repeatedly
// with the same upstream is a no-op; calling with a new upstream
// rebinds.
func startWindowsDNSProxy(upstream string) error {
	dnsProxyMu.Lock()
	defer dnsProxyMu.Unlock()

	if activeDNSProxy != nil {
		if activeDNSProxy.upstream == upstream {
			return nil // already running with the same config
		}
		// Upstream changed (rare — cert renewal usually doesn't move DNS).
		// Stop and start fresh.
		activeDNSProxy.shutdown()
		activeDNSProxy = nil
	}

	p := &windowsDNSProxy{upstream: upstream}

	// UDP is the common path; TCP is needed for large responses (EDNS fallback).
	p.udp = &dns.Server{
		Addr:         windowsDNSProxyAddr,
		Net:          "udp",
		Handler:      dns.HandlerFunc(p.handle),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	p.tcp = &dns.Server{
		Addr:         windowsDNSProxyAddr,
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
		return fmt.Errorf("dns proxy UDP bind %s: %w", windowsDNSProxyAddr, err)
	case err := <-tcpErr:
		return fmt.Errorf("dns proxy TCP bind %s: %w", windowsDNSProxyAddr, err)
	case <-time.After(500 * time.Millisecond):
	}

	activeDNSProxy = p
	log.Printf("[dns-proxy] listening on %s (udp+tcp) → upstream %s", windowsDNSProxyAddr, upstream)
	return nil
}

func stopWindowsDNSProxy() {
	dnsProxyMu.Lock()
	defer dnsProxyMu.Unlock()
	if activeDNSProxy == nil {
		return
	}
	activeDNSProxy.shutdown()
	activeDNSProxy = nil
	log.Printf("[dns-proxy] stopped")
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
