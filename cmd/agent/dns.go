package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// dnsConfig holds the persisted DNS configuration from enrollment.
type dnsConfig struct {
	Domain    string // e.g., "zero", "prod", "hop"
	ServerAddr string // e.g., "132.145.232.64:15300"
}

// readDNSConfig reads the persisted DNS configuration for one instance.
// Returns nil if DNS is not configured (no domain file).
func readDNSConfig(inst *meshInstance) *dnsConfig {
	dir := inst.dir()
	domain, err := os.ReadFile(filepath.Join(dir, "dns-domain"))
	if err != nil {
		return nil
	}
	server, err := os.ReadFile(filepath.Join(dir, "dns-server"))
	if err != nil {
		return nil
	}
	d := strings.TrimSpace(string(domain))
	s := strings.TrimSpace(string(server))
	if d == "" || s == "" {
		return nil
	}
	return &dnsConfig{Domain: d, ServerAddr: s}
}

// configureDNS sets up OS-level split-DNS so queries for the mesh domain
// (e.g., *.zero) are routed to the mesh DNS server. Only runs in kernel TUN mode
// since userspace mode doesn't have an OS-level network interface.
//
// Scoped to one meshInstance — multiple instances configure DNS
// independently. Platform implementations handle aggregation where
// needed (Windows per-domain loopback, Linux drop-in merger).
func configureDNS(inst *meshInstance, cfg *dnsConfig) {
	if cfg == nil {
		return
	}

	host, port, err := net.SplitHostPort(cfg.ServerAddr)
	if err != nil {
		log.Printf("[dns %s] invalid server address %q: %v", inst.name(), cfg.ServerAddr, err)
		return
	}

	if err := platformConfigureDNS(inst.name(), cfg.Domain, host, port); err != nil {
		log.Printf("[dns %s] WARNING: failed to configure split-DNS for .%s: %v", inst.name(), cfg.Domain, err)
		log.Printf("[dns %s] manual setup: point DNS for .%s to %s", inst.name(), cfg.Domain, cfg.ServerAddr)
		return
	}

	log.Printf("[dns %s] split-DNS configured: .%s → %s", inst.name(), cfg.Domain, cfg.ServerAddr)
}

// cleanupDNS removes OS-level DNS configuration on shutdown.
func cleanupDNS(inst *meshInstance, cfg *dnsConfig) {
	if cfg == nil {
		return
	}
	if err := platformCleanupDNS(inst.name(), cfg.Domain); err != nil {
		log.Printf("[dns %s] WARNING: failed to cleanup DNS for .%s: %v", inst.name(), cfg.Domain, err)
	} else {
		log.Printf("[dns %s] split-DNS removed for .%s", inst.name(), cfg.Domain)
	}
}

// dnsResolverFileContent generates the content for /etc/resolver/<domain> on macOS.
func dnsResolverFileContent(serverIP, port string) string {
	return fmt.Sprintf("nameserver %s\nport %s\n", serverIP, port)
}
