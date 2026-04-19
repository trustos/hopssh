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
func configureDNS(cfg *dnsConfig) {
	if cfg == nil {
		return
	}

	host, port, err := net.SplitHostPort(cfg.ServerAddr)
	if err != nil {
		log.Printf("[dns] invalid server address %q: %v", cfg.ServerAddr, err)
		return
	}

	if err := platformConfigureDNS(cfg.Domain, host, port); err != nil {
		log.Printf("[dns] WARNING: failed to configure split-DNS for .%s: %v", cfg.Domain, err)
		log.Printf("[dns] manual setup: point DNS for .%s to %s", cfg.Domain, cfg.ServerAddr)
		return
	}

	log.Printf("[dns] split-DNS configured: .%s → %s", cfg.Domain, cfg.ServerAddr)
}

// cleanupDNS removes OS-level DNS configuration on shutdown.
func cleanupDNS(cfg *dnsConfig) {
	if cfg == nil {
		return
	}
	if err := platformCleanupDNS(cfg.Domain); err != nil {
		log.Printf("[dns] WARNING: failed to cleanup DNS for .%s: %v", cfg.Domain, err)
	} else {
		log.Printf("[dns] split-DNS removed for .%s", cfg.Domain)
	}
}

// dnsResolverFileContent generates the content for /etc/resolver/<domain> on macOS.
func dnsResolverFileContent(serverIP, port string) string {
	return fmt.Sprintf("nameserver %s\nport %s\n", serverIP, port)
}
