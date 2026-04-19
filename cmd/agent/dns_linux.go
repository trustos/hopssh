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
	"sort"
	"strings"
	"sync"
	"time"
)

// dropInPath is the systemd-resolved drop-in config we write to force
// split-DNS via global config (bypasses the per-link-non-53-port bug
// in systemd-resolved seen on Ubuntu 25.10 / systemd 257+, also
// reported against older versions in NetBird #3443).
//
// Not a const so tests can point it at a temp file.
var dropInPath = "/etc/systemd/resolved.conf.d/hopssh.conf"

// dropInEntry is one instance's contribution to the merged drop-in.
type dropInEntry struct {
	domain string
	addr   string // host or host:port
}

// dropInState tracks every active instance's drop-in contribution so
// we can regenerate the merged `/etc/systemd/resolved.conf.d/hopssh.conf`
// on add/remove. The merged file is the only writer — individual
// instances never touch their own file.
var dropInState = struct {
	mu      sync.Mutex
	entries map[string]dropInEntry // keyed by instance name
}{entries: make(map[string]dropInEntry)}

// platformConfigureDNS configures split-DNS on Linux.
// Tries systemd-resolved per-link first (cheap, works on most systems
// when port is standard). If that registers but the stub doesn't
// forward (broken systemd-resolved case), falls back to a merged
// global drop-in config that works reliably across all configured
// instances. If systemd-resolved is not present at all, falls back to
// /etc/resolver/<domain> (rare distros).
func platformConfigureDNS(instanceName, domain, serverIP, port string) error {
	if _, err := exec.LookPath("resolvectl"); err == nil {
		if err := configureViaResolvectl(instanceName, domain, serverIP, port); err == nil {
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
// back to the merged drop-in config if the probe fails.
//
// The Nebula interface name is derived from the instance name
// (meshIfaceName) so N enrollments register their DNS on N distinct
// interfaces. Fallback to findNebulaInterface() covers legacy
// single-network configs where the interface may still be named
// nebula1 from a prior boot (the rewrite in ensureP2PConfig normalizes
// on next restart).
func configureViaResolvectl(instanceName, domain, serverIP, port string) error {
	iface := meshIfaceName(instanceName)
	if _, err := os.Stat("/sys/class/net/" + iface); err != nil {
		// Interface under expected name doesn't exist yet — fall back
		// to "first match" detection for pre-rewrite agents.
		if alt := findNebulaInterface(); alt != "" {
			iface = alt
		} else {
			return fmt.Errorf("no Nebula interface found (looked for %s)", iface)
		}
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

	if err := updateResolvedDropIn(instanceName, &dropInEntry{domain: domain, addr: addr}); err != nil {
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

// updateResolvedDropIn mutates the in-memory drop-in state (add when
// entry != nil, remove when entry == nil) and writes the merged file.
// The merged file has one [Resolve] block listing every active
// instance's DNS server + domain. Entries are sorted by instance name
// for deterministic output — preventing spurious file churn when
// multiple instances race to register.
func updateResolvedDropIn(instanceName string, entry *dropInEntry) error {
	dropInState.mu.Lock()
	defer dropInState.mu.Unlock()

	if entry == nil {
		delete(dropInState.entries, instanceName)
	} else {
		dropInState.entries[instanceName] = *entry
	}

	if len(dropInState.entries) == 0 {
		// Last instance cleaned up → remove the file.
		if err := os.Remove(dropInPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}

	dir := filepath.Dir(dropInPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	names := make([]string, 0, len(dropInState.entries))
	for name := range dropInState.entries {
		names = append(names, name)
	}
	sort.Strings(names)

	var dnsAddrs, domains []string
	for _, name := range names {
		e := dropInState.entries[name]
		dnsAddrs = append(dnsAddrs, e.addr)
		domains = append(domains, "~"+e.domain)
	}

	content := fmt.Sprintf(
		"# Written by hop-agent. Merged across enrollments: %s. Safe to remove; regenerated on next enroll.\n[Resolve]\nDNS=%s\nDomains=%s\n",
		strings.Join(names, ", "),
		strings.Join(dnsAddrs, " "),
		strings.Join(domains, " "),
	)
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

// platformCleanupDNS removes DNS configuration on Linux for one instance.
// Handles both the per-link registration and this instance's slice of
// the drop-in config, since the agent may have configured either path
// (or both across upgrades).
func platformCleanupDNS(instanceName, domain string) error {
	// Pull this instance's drop-in entry out of the merged file and
	// regenerate. If that was the last entry, the file gets removed.
	if err := updateResolvedDropIn(instanceName, nil); err != nil {
		return fmt.Errorf("update drop-in on cleanup: %w", err)
	}
	// Reload systemd-resolved so the stub picks up the regenerated
	// (or absent) drop-in — best effort, safe to skip on distros
	// without systemd.
	_ = exec.Command("systemctl", "reload-or-restart", "systemd-resolved").Run()

	// Revert per-link config if present. Note: with multi-instance
	// kernel-TUN mode each instance has its own interface, so this
	// only touches the one interface this instance set up.
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
