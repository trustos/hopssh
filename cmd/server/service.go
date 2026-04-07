package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	serverServiceName = "hopssh"
	serverSystemdPath = "/etc/systemd/system/hopssh.service"
	serverLaunchdPath = "Library/LaunchAgents/com.hopssh.server.plist"
)

func runServerInstall(args []string) {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	endpoint := fs.String("endpoint", "", "Public URL of this server (required)")
	dataDir := fs.String("data", "/var/lib/hopssh", "Data directory")
	addr := fs.String("addr", ":9473", "Listen address")
	fs.Parse(args)

	if *endpoint == "" {
		fmt.Fprintf(os.Stderr, "Error: --endpoint is required.\n\n")
		fmt.Fprintf(os.Stderr, "Example:\n")
		fmt.Fprintf(os.Stderr, "  sudo hop-server install --endpoint http://YOUR_PUBLIC_IP:9473\n\n")
		fmt.Fprintf(os.Stderr, "The endpoint is the URL that agents and clients use to reach this server.\n")
		os.Exit(1)
	}

	// Create data directory.
	if err := os.MkdirAll(*dataDir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "Error: Cannot create data directory %s: %v\n", *dataDir, err)
		fmt.Fprintf(os.Stderr, "Run with sudo: sudo hop-server install --endpoint %s\n", *endpoint)
		os.Exit(1)
	}

	switch runtime.GOOS {
	case "linux":
		installServerSystemd(*endpoint, *dataDir, *addr)
	case "darwin":
		installServerLaunchd(*endpoint, *dataDir, *addr)
	default:
		fmt.Fprintf(os.Stderr, "Error: Unsupported operating system: %s\n", runtime.GOOS)
		fmt.Fprintf(os.Stderr, "Start manually: hop-server --endpoint %s --data %s\n", *endpoint, *dataDir)
		os.Exit(1)
	}
}

func serverSystemdUnit(endpoint, dataDir, addr string) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=hopssh control plane\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	fmt.Fprintf(&b, "ExecStart=/usr/local/bin/hop-server --data %s --endpoint %s --addr %s\n", dataDir, endpoint, addr)
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=5\n")
	b.WriteString("LimitNOFILE=65536\n\n")
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")
	return b.String()
}

func installServerSystemd(endpoint, dataDir, addr string) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		fmt.Fprintf(os.Stderr, "Error: systemctl not found. Is systemd available?\n")
		fmt.Fprintf(os.Stderr, "Start manually: hop-server --endpoint %s --data %s\n", endpoint, dataDir)
		os.Exit(1)
	}

	unit := serverSystemdUnit(endpoint, dataDir, addr)
	if err := os.WriteFile(serverSystemdPath, []byte(unit), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error: Cannot write %s: %v\n", serverSystemdPath, err)
		fmt.Fprintf(os.Stderr, "Run with sudo: sudo hop-server install --endpoint %s\n", endpoint)
		os.Exit(1)
	}

	cmds := [][]string{
		{"systemctl", "daemon-reload"},
		{"systemctl", "enable", serverServiceName},
		{"systemctl", "start", serverServiceName},
	}
	for _, c := range cmds {
		if out, err := exec.Command(c[0], c[1:]...).CombinedOutput(); err != nil {
			log.Fatalf("Failed to run %v: %v\n%s", c, err, out)
		}
	}

	fmt.Println("==> hopssh control plane installed and started.")
	fmt.Printf("    Dashboard: %s\n", endpoint)
	fmt.Printf("    Data:      %s\n", dataDir)
	fmt.Println("    Status:    sudo systemctl status hopssh")
	fmt.Println("    Logs:      journalctl -u hopssh -f")
}

func serverLaunchdPlist(endpoint, dataDir, addr string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.hopssh.server</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/hop-server</string>
    <string>--data</string>
    <string>` + dataDir + `</string>
    <string>--endpoint</string>
    <string>` + endpoint + `</string>
    <string>--addr</string>
    <string>` + addr + `</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>/var/log/hopssh.log</string>
  <key>StandardErrorPath</key>
  <string>/var/log/hopssh.log</string>
</dict>
</plist>
`
}

func installServerLaunchd(endpoint, dataDir, addr string) {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("Cannot determine home directory: %v", err)
	}
	plistPath := filepath.Join(home, serverLaunchdPath)

	if err := os.MkdirAll(filepath.Dir(plistPath), 0755); err != nil {
		log.Fatalf("Cannot create LaunchAgents directory: %v", err)
	}

	plist := serverLaunchdPlist(endpoint, dataDir, addr)
	if err := os.WriteFile(plistPath, []byte(plist), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error: Cannot write %s: %v\n", plistPath, err)
		os.Exit(1)
	}

	if out, err := exec.Command("launchctl", "load", plistPath).CombinedOutput(); err != nil {
		log.Fatalf("Failed to load launchd service: %v\n%s", err, out)
	}

	fmt.Println("==> hopssh control plane installed and started.")
	fmt.Printf("    Dashboard: %s\n", endpoint)
	fmt.Printf("    Data:      %s\n", dataDir)
	fmt.Printf("    Plist:     %s\n", plistPath)
	fmt.Println("    Logs:      /var/log/hopssh.log")
}

func runServerUninstall(args []string) {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	purge := fs.Bool("purge", false, "Also remove data directory (/var/lib/hopssh/)")
	fs.Parse(args)

	switch runtime.GOOS {
	case "linux":
		exec.Command("systemctl", "stop", serverServiceName).Run()
		exec.Command("systemctl", "disable", serverServiceName).Run()
		os.Remove(serverSystemdPath)
		exec.Command("systemctl", "daemon-reload").Run()
	case "darwin":
		home, _ := os.UserHomeDir()
		plistPath := filepath.Join(home, serverLaunchdPath)
		exec.Command("launchctl", "unload", plistPath).Run()
		os.Remove(plistPath)
	}

	fmt.Println("==> hopssh service uninstalled.")

	if *purge {
		dataDir := "/var/lib/hopssh"
		if err := os.RemoveAll(dataDir); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Could not remove %s: %v\n", dataDir, err)
		} else {
			fmt.Printf("    Removed: %s\n", dataDir)
		}
	}
}
