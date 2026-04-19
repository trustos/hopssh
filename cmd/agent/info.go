package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/trustos/hopssh/internal/buildinfo"
)

func runInfo(args []string) {
	loadPrimaryEnrollment()

	hostname, _ := os.Hostname()

	dir := activeEnrollDir()
	nodeID := "not enrolled"
	if data, err := os.ReadFile(filepath.Join(dir, "node-id")); err == nil {
		nodeID = strings.TrimSpace(string(data))
	}

	endpoint := "not configured"
	if data, err := os.ReadFile(filepath.Join(dir, "endpoint")); err == nil {
		endpoint = strings.TrimSpace(string(data))
	}

	dnsName := sanitizeDNSName(hostname)

	fmt.Printf("Hostname:   %s\n", hostname)
	fmt.Printf("DNS Name:   %s\n", dnsName)
	fmt.Printf("OS:         %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Printf("Node ID:    %s\n", nodeID)
	fmt.Printf("Endpoint:   %s\n", endpoint)
	fmt.Printf("Config:     %s\n", configDir)
	fmt.Printf("Version:    %s (%s)\n", buildinfo.Version, buildinfo.Commit)
}

// sanitizeDNSName converts a hostname to a DNS-safe short name.
func sanitizeDNSName(hostname string) string {
	name := strings.ToLower(hostname)
	if idx := strings.Index(name, "."); idx >= 0 {
		name = name[:idx]
	}
	// Simple replacement of non-alphanumeric with hyphens.
	var result []byte
	for _, c := range []byte(name) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			result = append(result, c)
		} else {
			result = append(result, '-')
		}
	}
	name = strings.Trim(string(result), "-")
	if name == "" {
		return "node"
	}
	return name
}
