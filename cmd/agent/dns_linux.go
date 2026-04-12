package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// platformConfigureDNS configures split-DNS on Linux.
// Tries systemd-resolved first, falls back to /etc/resolver/<domain>.
func platformConfigureDNS(domain, serverIP, port string) error {
	// Try systemd-resolved (most modern Linux distros).
	if _, err := exec.LookPath("resolvectl"); err == nil {
		// resolvectl needs an interface name. We use the Nebula TUN interface.
		iface := findNebulaInterface()
		if iface != "" {
			addr := serverIP
			if port != "53" {
				addr = fmt.Sprintf("%s:%s", serverIP, port)
			}
			if err := exec.Command("resolvectl", "dns", iface, addr).Run(); err != nil {
				return fmt.Errorf("resolvectl dns: %w", err)
			}
			if err := exec.Command("resolvectl", "domain", iface, "~"+domain).Run(); err != nil {
				return fmt.Errorf("resolvectl domain: %w", err)
			}
			return nil
		}
	}

	// Fallback: write /etc/resolver/<domain> (works on some distros).
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

// platformCleanupDNS removes DNS configuration on Linux.
func platformCleanupDNS(domain string) error {
	// Try systemd-resolved cleanup.
	if _, err := exec.LookPath("resolvectl"); err == nil {
		iface := findNebulaInterface()
		if iface != "" {
			exec.Command("resolvectl", "revert", iface).Run()
			return nil
		}
	}

	// Fallback: remove resolver file.
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
