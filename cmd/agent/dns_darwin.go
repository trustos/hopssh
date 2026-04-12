package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// platformConfigureDNS creates /etc/resolver/<domain> for macOS split-DNS.
// macOS automatically routes queries for *.<domain> to the specified nameserver.
func platformConfigureDNS(domain, serverIP, port string) error {
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

// platformCleanupDNS removes the /etc/resolver/<domain> file.
func platformCleanupDNS(domain string) error {
	path := filepath.Join("/etc/resolver", domain)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
