package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/slackhq/nebula/cert"
)

func runStatus(args []string) {
	loadPrimaryEnrollment()

	// Read cert for IP and expiry.
	dir := activeEnrollDir()
	certPath := filepath.Join(dir, "node.crt")
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		fmt.Printf("Status:     not enrolled\n")
		fmt.Printf("Config:     %s\n", configDir)
		fmt.Println("\nRun 'hop-agent enroll --endpoint <url>' to join a network.")
		return
	}

	c, _, err := cert.UnmarshalCertificateFromPEM(certPEM)
	if err != nil {
		fmt.Printf("Status:     error (invalid cert: %v)\n", err)
		return
	}

	nebulaIP := "unknown"
	networks := c.Networks()
	if len(networks) > 0 {
		nebulaIP = networks[0].String()
	}

	notAfter := c.NotAfter()
	remaining := time.Until(notAfter)
	certStatus := "valid"
	if remaining <= 0 {
		certStatus = "EXPIRED"
	} else if remaining < 2*time.Hour {
		certStatus = fmt.Sprintf("valid (expires in %s — renewal imminent)", remaining.Truncate(time.Minute))
	} else {
		certStatus = fmt.Sprintf("valid (expires in %s)", remaining.Truncate(time.Minute))
	}

	groups := c.Groups()

	// Read endpoint.
	endpoint := "unknown"
	if data, err := os.ReadFile(filepath.Join(dir, "endpoint")); err == nil {
		endpoint = strings.TrimSpace(string(data))
	}

	// Read node ID.
	nodeID := "unknown"
	if data, err := os.ReadFile(filepath.Join(dir, "node-id")); err == nil {
		nodeID = strings.TrimSpace(string(data))
	}

	// Check if service is running.
	serviceStatus := "unknown"
	if runtime.GOOS == "darwin" {
		if err := exec.Command("launchctl", "list", "com.hopssh.agent").Run(); err == nil {
			serviceStatus = "running (launchd)"
		} else {
			serviceStatus = "stopped"
		}
	} else {
		out, err := exec.Command("systemctl", "is-active", "hop-agent").Output()
		if err == nil {
			serviceStatus = strings.TrimSpace(string(out)) + " (systemd)"
		} else {
			serviceStatus = "stopped"
		}
	}

	fmt.Printf("Service:    %s\n", serviceStatus)
	fmt.Printf("Nebula IP:  %s\n", nebulaIP)
	fmt.Printf("Endpoint:   %s\n", endpoint)
	fmt.Printf("Node ID:    %s\n", nodeID)
	fmt.Printf("Cert:       %s\n", certStatus)
	fmt.Printf("Groups:     %s\n", strings.Join(groups, ", "))
	fmt.Printf("Config:     %s\n", configDir)
}
