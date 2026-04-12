//go:build !darwin && !linux && !windows

package main

import "fmt"

func platformConfigureDNS(domain, serverIP, port string) error {
	return fmt.Errorf("split-DNS not supported on this platform; manually configure DNS for .%s to %s:%s", domain, serverIP, port)
}

func platformCleanupDNS(domain string) error {
	return nil
}
