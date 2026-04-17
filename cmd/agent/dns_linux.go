//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// dropInPath is the systemd-resolved drop-in config we write to force
// split-DNS via global config (bypasses the per-link-non-53-port bug
// in systemd-resolved seen on Ubuntu 25.10 / systemd 257+, also
// reported against older versions in NetBird #3443).
const dropInPath = "/etc/systemd/resolved.conf.d/hopssh.conf"

// platformConfigureDNS configures split-DNS on Linux.
// Tries systemd-resolved per-link first (cheap, works on most systems
// when port is standard). If that registers but the stub doesn't
// forward (broken systemd-resolved case), falls back to a global
// drop-in config that works reliably. If systemd-resolved is not
// present at all, falls back to /etc/resolver/<domain> (rare distros).
func platformConfigureDNS(domain, serverIP, port string) error {
	if _, err := exec.LookPath("resolvectl"); err == nil {
		if err := configureViaResolvectl(domain, serverIP, port); err == nil {
			return nil
		}
	}

	// Final fallback: write /etc/resolver/<domain>. Works on some distros
	// without systemd-resolved (or where resolvectl isn't available).
	resolverDir := "/etc/resolver"
	if err := os.MkdirAll(resolverDir, 0755); err != nil {
		return fmt.Errorf("create %s: %w", resolverDir, err)
	}
	content := dnsResolverFileContent(serverIP, port)
	path := filepath.Join(resolverDir, domain)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// configureViaResolvectl tries the per-link resolvectl approach first,
// probes the stub to verify it actually forwards queries, and falls
// back to a global drop-in config if the probe fails. This targets the
// known broken case (systemd-resolved stub silently dropping `.domain`
// queries when DNS server port is non-53) without regressing working
// systems.
func configureViaResolvectl(domain, serverIP, port string) error {
	iface := findNebulaInterface()
	if iface == "" {
		return fmt.Errorf("no Nebula interface found")
	}

	addr := serverIP
	if port != "" && port != "53" {
		addr = fmt.Sprintf("%s:%s", serverIP, port)
	}

	// Register per-link first.
	if err := exec.Command("resolvectl", "dns", iface, addr).Run(); err != nil {
		return fmt.Errorf("resolvectl dns: %w", err)
	}
	if err := exec.Command("resolvectl", "domain", iface, "~"+domain).Run(); err != nil {
		return fmt.Errorf("resolvectl domain: %w", err)
	}

	// Standard port 53 has no known forwarding issues — skip probe.
	if port == "" || port == "53" {
		return nil
	}

	// Non-standard port: probe the stub to verify forwarding actually works.
	if stubForwardsQueries(domain, 2*time.Second) {
		return nil
	}

	log.Printf("[dns] per-link DNS registered on %s but stub not forwarding queries; switching to systemd-resolved drop-in config", iface)

	// Revert the per-link registration so the global drop-in takes effect
	// cleanly without conflicting per-link state.
	_ = exec.Command("resolvectl", "revert", iface).Run()

	if err := writeResolvedDropIn(domain, addr); err != nil {
		return fmt.Errorf("write drop-in: %w", err)
	}
	// reload-or-restart is more portable than reload across systemd versions.
	if err := exec.Command("systemctl", "reload-or-restart", "systemd-resolved").Run(); err != nil {
		return fmt.Errorf("reload systemd-resolved: %w", err)
	}

	// Verify the drop-in works. If this also fails, the user has a deeper
	// issue (e.g., systemd-resolved not running, firewall blocking outbound
	// UDP to the DNS server) — surface it as an error so it's diagnosable.
	if !stubForwardsQueries(domain, 3*time.Second) {
		return fmt.Errorf("drop-in config written but stub still not forwarding (systemd-resolved running?)")
	}
	return nil
}

// writeResolvedDropIn writes the systemd-resolved drop-in config that
// registers our DNS server in the global scope with a domain route.
func writeResolvedDropIn(domain, addr string) error {
	dir := filepath.Dir(dropInPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	content := fmt.Sprintf("# Written by hop-agent. Safe to remove; regenerated on next enroll.\n[Resolve]\nDNS=%s\nDomains=~%s\n", addr, domain)
	return os.WriteFile(dropInPath, []byte(content), 0644)
}

// stubForwardsQueries sends a single UDP DNS query to 127.0.0.53:53
// for a random subdomain of the given domain and returns true if the
// stub produces any DNS response (NXDOMAIN, SERVFAIL, answer — all OK),
// false if it times out. Times out is the signature of the bug we're
// detecting: the stub accepted the query but silently dropped it.
func stubForwardsQueries(domain string, timeout time.Duration) bool {
	probe := fmt.Sprintf("hop-dns-probe-%d.%s", time.Now().UnixNano(), domain)
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return net.DialTimeout("udp", "127.0.0.53:53", timeout)
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	_, err := r.LookupHost(ctx, probe)
	if err == nil {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		// Any resolver response (NXDOMAIN included) means the stub forwarded.
		// Only a timeout proves the stub is broken for this DNS setup.
		return !dnsErr.IsTimeout
	}
	// Non-DNS error (ctx cancelled, etc.) — treat as failure.
	return false
}

// platformCleanupDNS removes DNS configuration on Linux. Handles both
// the per-link registration and the drop-in config, since the agent
// may have configured either path (or both across upgrades).
func platformCleanupDNS(domain string) error {
	// Remove drop-in config if present and reload systemd-resolved.
	if _, err := os.Stat(dropInPath); err == nil {
		_ = os.Remove(dropInPath)
		_ = exec.Command("systemctl", "reload-or-restart", "systemd-resolved").Run()
	}

	// Revert per-link config if present.
	if _, err := exec.LookPath("resolvectl"); err == nil {
		if iface := findNebulaInterface(); iface != "" {
			_ = exec.Command("resolvectl", "revert", iface).Run()
		}
	}

	// Remove /etc/resolver/<domain> fallback if present.
	path := filepath.Join("/etc/resolver", domain)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// findNebulaInterface finds the Nebula TUN interface name by looking for
// interfaces starting with "nebula" or "tun" that aren't the loopback.
func findNebulaInterface() string {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return ""
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "nebula") || strings.HasPrefix(name, "hop") {
			return name
		}
	}
	// Check for generic tun devices.
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "tun") && e.Name() != "tunl0" {
			return e.Name()
		}
	}
	return ""
}
