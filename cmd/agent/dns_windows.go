package main

import (
	"fmt"
	"os/exec"
)

// platformConfigureDNS configures split-DNS on Windows using NRPT rules.
// NRPT (Name Resolution Policy Table) routes queries for the mesh domain
// to the control plane's DNS server without affecting other DNS resolution.
func platformConfigureDNS(domain, serverIP, port string) error {
	// PowerShell: Add-DnsClientNrptRule -Namespace ".<domain>" -NameServers "<ip>"
	// Note: NRPT doesn't support custom ports natively. If the DNS server
	// runs on a non-standard port, we'd need a local forwarder. For now,
	// configure the rule pointing to the server IP (works when port is 53).
	namespace := "." + domain
	ps := fmt.Sprintf(
		`Add-DnsClientNrptRule -Namespace "%s" -NameServers "%s" -Comment "hopssh mesh DNS"`,
		namespace, serverIP,
	)
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("NRPT rule: %w (%s)", err, string(out))
	}
	return nil
}

// platformCleanupDNS removes the NRPT rule for the mesh domain.
func platformCleanupDNS(domain string) error {
	namespace := "." + domain
	ps := fmt.Sprintf(
		`Get-DnsClientNrptRule | Where-Object { $_.Namespace -eq "%s" -and $_.Comment -eq "hopssh mesh DNS" } | Remove-DnsClientNrptRule -Force`,
		namespace,
	)
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("remove NRPT rule: %w (%s)", err, string(out))
	}
	return nil
}
