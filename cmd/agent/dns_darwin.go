//go:build darwin

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// platformConfigureDNS creates /etc/resolver/<domain> for macOS split-DNS.
// macOS automatically routes queries for *.<domain> to the specified
// nameserver. One file per domain — already multi-instance safe.
// instanceName is accepted for API uniformity with other platforms.
func platformConfigureDNS(instanceName, domain, serverIP, port string) error {
	_ = instanceName
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
func platformCleanupDNS(instanceName, domain string) error {
	_ = instanceName
	path := filepath.Join("/etc/resolver", domain)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
