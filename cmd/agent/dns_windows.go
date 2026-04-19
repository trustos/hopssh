//go:build windows

package main

import (
	"fmt"
	"os/exec"
)

// platformConfigureDNS configures split-DNS on Windows.
//
// Windows NRPT (Name Resolution Policy Table) doesn't carry a port in
// `-NameServers`; the port is silently stripped. Our lighthouse DNS runs
// on a non-standard port (15300), so we can't register it directly —
// Windows would query :53 and time out.
//
// Workaround: start a local DNS forwarder on a dedicated loopback IP
// (one per instance, in 127.53.0.0/24), then register that IP in NRPT
// (port 53 is the NRPT default). Multi-instance-safe: each instance
// gets its own loopback so queries for .<instance-domain> reach the
// right upstream.
//
// Also cleans up any stale hopssh NRPT rules for this domain from
// previous runs to avoid the accumulation we observed during
// validation (multiple identical rules for .<domain>). Tag-based
// removal via the `Comment` field — only hopssh-written rules get
// touched.
func platformConfigureDNS(instanceName, domain, serverIP, port string) error {
	// Upstream for the local forwarder. Always include the port —
	// miekg/dns-Client expects <host>:<port>.
	upstream := serverIP + ":53"
	if port != "" && port != "53" {
		upstream = fmt.Sprintf("%s:%s", serverIP, port)
	}

	// Remove any stale hopssh NRPT rules for this domain from previous
	// runs BEFORE starting the proxy (so we don't stop-then-start the
	// one we're about to use).
	_ = removeHopsshNRPTRules(domain)

	loopback, err := startWindowsDNSProxy(instanceName, upstream)
	if err != nil {
		return fmt.Errorf("start DNS proxy: %w", err)
	}

	// Register the loopback proxy address. NRPT doesn't need a port —
	// the DNS client queries :53 which is where our proxy listens.
	namespace := "." + domain
	ps := fmt.Sprintf(
		`Add-DnsClientNrptRule -Namespace "%s" -NameServers "%s" -Comment "hopssh mesh DNS"`,
		namespace, loopback,
	)
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Tear down the proxy on NRPT registration failure so we don't leak
		// the :53 listener.
		stopWindowsDNSProxy(instanceName)
		return fmt.Errorf("NRPT rule: %w (%s)", err, string(out))
	}
	return nil
}

// platformCleanupDNS removes this instance's NRPT rule and stops its
// local DNS forwarder. Uses Comment-based targeting so only
// hopssh-created rules are touched; other instances' rules are left
// intact.
func platformCleanupDNS(instanceName, domain string) error {
	stopWindowsDNSProxy(instanceName)
	return removeHopsshNRPTRules(domain)
}

// removeHopsshNRPTRules deletes any NRPT rules we've written for this
// domain. Used both at startup (to clear stale rules from prior runs)
// and at shutdown. Does NOT touch the DNS proxy.
func removeHopsshNRPTRules(domain string) error {
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
