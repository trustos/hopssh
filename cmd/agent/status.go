package main

import (
	"flag"
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
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	network := fs.String("network", "", "Show status for one enrollment (default: all)")
	cfgDir := fs.String("config-dir", "", "Override config directory")
	fs.Parse(args)

	if *cfgDir != "" {
		configDir = resolveConfigDir(*cfgDir)
	}

	reg := loadPrimaryEnrollment()

	if reg.Len() == 0 {
		fmt.Printf("Status:     not enrolled\n")
		fmt.Printf("Config:     %s\n", configDir)
		fmt.Println("\nRun 'hop-agent enroll --endpoint <url>' to join a network.")
		return
	}

	serviceStatus := readServiceStatus()

	filter := strings.TrimSpace(*network)
	var targets []*Enrollment
	if filter != "" {
		e := reg.Get(filter)
		if e == nil {
			fmt.Fprintf(os.Stderr, "No enrollment named %q (available: %v)\n", filter, reg.Names())
			os.Exit(1)
		}
		targets = []*Enrollment{e}
	} else {
		targets = reg.List()
	}

	fmt.Printf("Service:    %s\n", serviceStatus)
	fmt.Printf("Config:     %s\n", configDir)
	fmt.Printf("Enrollments (%d):\n", len(targets))
	for i, e := range targets {
		if i > 0 {
			fmt.Println()
		}
		printEnrollmentStatus(e)
	}
}

func printEnrollmentStatus(e *Enrollment) {
	dir := enrollmentDir(configDir, e.Name)
	fmt.Printf("  • %s\n", e.Name)
	fmt.Printf("      Endpoint:   %s\n", e.Endpoint)
	fmt.Printf("      Node ID:    %s\n", e.NodeID)
	if e.DNSDomain != "" {
		fmt.Printf("      DNS:        .%s\n", e.DNSDomain)
	}

	certPath := filepath.Join(dir, "node.crt")
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		fmt.Printf("      Cert:       missing (%s)\n", certPath)
		return
	}
	c, _, err := cert.UnmarshalCertificateFromPEM(certPEM)
	if err != nil {
		fmt.Printf("      Cert:       invalid (%v)\n", err)
		return
	}

	nebulaIP := "unknown"
	networks := c.Networks()
	if len(networks) > 0 {
		nebulaIP = networks[0].String()
	}
	remaining := time.Until(c.NotAfter())
	certStatus := "valid"
	switch {
	case remaining <= 0:
		certStatus = "EXPIRED"
	case remaining < 2*time.Hour:
		certStatus = fmt.Sprintf("valid (expires in %s — renewal imminent)", remaining.Truncate(time.Minute))
	default:
		certStatus = fmt.Sprintf("valid (expires in %s)", remaining.Truncate(time.Minute))
	}
	groups := c.Groups()

	fmt.Printf("      Nebula IP:  %s\n", nebulaIP)
	fmt.Printf("      Cert:       %s\n", certStatus)
	if len(groups) > 0 {
		fmt.Printf("      Groups:     %s\n", strings.Join(groups, ", "))
	}
	if e.TunMode != "" {
		fmt.Printf("      TUN mode:   %s\n", e.TunMode)
	}
}

func readServiceStatus() string {
	switch runtime.GOOS {
	case "darwin":
		if err := exec.Command("launchctl", "list", "com.hopssh.agent").Run(); err == nil {
			return "running (launchd)"
		}
		return "stopped"
	case "windows":
		if out, err := exec.Command("sc.exe", "query", "hop-agent").CombinedOutput(); err == nil && strings.Contains(string(out), "RUNNING") {
			return "running (sc.exe)"
		}
		return "stopped"
	default:
		out, err := exec.Command("systemctl", "is-active", "hop-agent").Output()
		if err == nil {
			return strings.TrimSpace(string(out)) + " (systemd)"
		}
		return "stopped"
	}
}
