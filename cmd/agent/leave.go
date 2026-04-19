package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// runLeave handles `hop-agent leave --network <name>`. Removes one
// enrollment from the local registry and wipes its on-disk state
// without touching any other enrollments on the same agent.
//
// The control plane keeps a record of the node — it just goes offline
// once heartbeats stop. Operators can delete the stale node from the
// dashboard; we don't ship an agent→server "delete me" endpoint in
// v0.10.0 (would require a server-side API change per the plan).
func runLeave(args []string) {
	fs := flag.NewFlagSet("leave", flag.ExitOnError)
	network := fs.String("network", "", "Enrollment name to leave (required when multiple exist)")
	yes := fs.Bool("yes", false, "Skip the confirmation prompt")
	cfgDir := fs.String("config-dir", "", "Override config directory")
	fs.Parse(args)

	if *cfgDir != "" {
		configDir = resolveConfigDir(*cfgDir)
	}

	if _, err := migrateLegacyLayout(configDir); err != nil {
		log.Fatalf("Legacy config migration failed: %v", err)
	}

	reg, err := loadEnrollmentRegistry(configDir)
	if err != nil {
		log.Fatalf("Load enrollments: %v", err)
	}

	if reg.Len() == 0 {
		fmt.Println("No enrollments to leave.")
		return
	}

	targetName := strings.TrimSpace(*network)
	if targetName == "" {
		if reg.Len() > 1 {
			fmt.Fprintf(os.Stderr, "Multiple enrollments present (%v); specify --network <name>.\n", reg.Names())
			os.Exit(1)
		}
		targetName = reg.List()[0].Name
	}

	target := reg.Get(targetName)
	if target == nil {
		fmt.Fprintf(os.Stderr, "No enrollment named %q (available: %v)\n", targetName, reg.Names())
		os.Exit(1)
	}

	if !*yes {
		fmt.Printf("This will remove enrollment %q (node %s, network %s).\n", target.Name, target.NodeID, target.Endpoint)
		fmt.Print("Continue? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.ToLower(strings.TrimSpace(answer))
		if answer != "y" && answer != "yes" {
			fmt.Println("Cancelled.")
			return
		}
	}

	// Stop the service so the running agent doesn't race with us on
	// the subdir. After we finish we'll start it again if any other
	// enrollments remain.
	stopAgentService()

	// Clean up platform DNS for this instance (best-effort — the
	// service may not have been running, in which case configureDNS
	// was never called and cleanup is a no-op).
	if cfg := readDNSConfigForEnrollment(target); cfg != nil {
		stubInst := newMeshInstance(target)
		_ = platformCleanupDNS(stubInst.name(), cfg.Domain)
	}

	// Remove from registry first so a partial subdir delete still
	// leaves the registry consistent with what's on disk.
	if err := reg.Remove(target.Name); err != nil {
		log.Fatalf("Remove enrollment: %v", err)
	}

	// Delete the subdir. Safety: refuse to remove a path that's too
	// short, matches the suspicious-delete guard in enroll --force.
	subdir := enrollmentDir(configDir, target.Name)
	if len(subdir) > 5 {
		if err := os.RemoveAll(subdir); err != nil {
			log.Printf("WARNING: failed to remove %s: %v", subdir, err)
		}
	}

	fmt.Printf("  ✓ Left %q\n", target.Name)
	fmt.Printf("  Note: the control plane will show this node as offline until an admin removes it from the dashboard.\n")

	// Restart the service if other enrollments remain.
	if reg.Len() > 0 {
		startAgentService()
	}
}

// readDNSConfigForEnrollment reads the DNS config file off disk for an
// enrollment without needing a live meshInstance. Used during leave
// where we don't construct a full instance.
func readDNSConfigForEnrollment(e *Enrollment) *dnsConfig {
	tmp := newMeshInstance(e)
	return readDNSConfig(tmp)
}

// startAgentService starts the installed agent service. Mirror of
// stopAgentService in enroll.go.
func startAgentService() {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("launchctl", "load", "/Library/LaunchDaemons/com.hopssh.agent.plist").Run()
	case "windows":
		exec.Command("sc.exe", "start", "hop-agent").Run()
	default:
		exec.Command("systemctl", "start", "hop-agent").Run()
	}
}
