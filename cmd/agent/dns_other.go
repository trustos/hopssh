//go:build !darwin && !linux && !windows

package main

import "fmt"

func platformConfigureDNS(instanceName, domain, serverIP, port string) error {
	_ = instanceName
	return fmt.Errorf("split-DNS not supported on this platform; manually configure DNS for .%s to %s:%s", domain, serverIP, port)
}

func platformCleanupDNS(instanceName, domain string) error {
	_ = instanceName
	return nil
}
