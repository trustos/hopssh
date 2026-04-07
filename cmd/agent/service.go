package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

const (
	agentServiceName  = "hop-agent"
	agentSystemdPath  = "/etc/systemd/system/hop-agent.service"
	agentLaunchdPath  = "Library/LaunchAgents/com.hopssh.agent.plist"
	agentConfigDir    = "/etc/hop-agent"
)

const agentSystemdUnit = `[Unit]
Description=hopssh agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/hop-agent serve
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`

const agentLaunchdPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.hopssh.agent</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/hop-agent</string>
    <string>serve</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>/var/log/hop-agent.log</string>
  <key>StandardErrorPath</key>
  <string>/var/log/hop-agent.log</string>
</dict>
</plist>
`

func runAgentInstall(args []string) {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	fs.Parse(args)

	// Verify enrollment has been completed.
	requiredFiles := []string{"token", "endpoint", "node-id", "ca.crt", "node.crt"}
	for _, f := range requiredFiles {
		path := filepath.Join(agentConfigDir, f)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Error: Enrollment not complete — %s not found.\n\n", path)
			fmt.Fprintf(os.Stderr, "Run 'hop-agent enroll' first:\n")
			fmt.Fprintf(os.Stderr, "  sudo hop-agent enroll --endpoint https://your-control-plane:9473\n\n")
			os.Exit(1)
		}
	}

	switch runtime.GOOS {
	case "linux":
		installAgentSystemd()
	case "darwin":
		installAgentLaunchd()
	default:
		fmt.Fprintf(os.Stderr, "Error: Unsupported operating system: %s\n", runtime.GOOS)
		fmt.Fprintf(os.Stderr, "Start manually: hop-agent serve\n")
		os.Exit(1)
	}
}

func installAgentSystemd() {
	if _, err := exec.LookPath("systemctl"); err != nil {
		fmt.Fprintf(os.Stderr, "Error: systemctl not found. Is systemd available?\n")
		fmt.Fprintf(os.Stderr, "Start manually: hop-agent serve\n")
		os.Exit(1)
	}

	if err := os.WriteFile(agentSystemdPath, []byte(agentSystemdUnit), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error: Cannot write %s: %v\n", agentSystemdPath, err)
		fmt.Fprintf(os.Stderr, "Run with sudo: sudo hop-agent install\n")
		os.Exit(1)
	}

	cmds := [][]string{
		{"systemctl", "daemon-reload"},
		{"systemctl", "enable", agentServiceName},
		{"systemctl", "start", agentServiceName},
	}
	for _, c := range cmds {
		if out, err := exec.Command(c[0], c[1:]...).CombinedOutput(); err != nil {
			log.Fatalf("Failed to run %v: %v\n%s", c, err, out)
		}
	}

	fmt.Println("==> hop-agent service installed and started.")
	fmt.Println("    Status:  sudo systemctl status hop-agent")
	fmt.Println("    Logs:    journalctl -u hop-agent -f")
}

func installAgentLaunchd() {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("Cannot determine home directory: %v", err)
	}
	plistPath := filepath.Join(home, agentLaunchdPath)

	if err := os.MkdirAll(filepath.Dir(plistPath), 0755); err != nil {
		log.Fatalf("Cannot create LaunchAgents directory: %v", err)
	}

	if err := os.WriteFile(plistPath, []byte(agentLaunchdPlist), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error: Cannot write %s: %v\n", plistPath, err)
		os.Exit(1)
	}

	if out, err := exec.Command("launchctl", "load", plistPath).CombinedOutput(); err != nil {
		log.Fatalf("Failed to load launchd service: %v\n%s", err, out)
	}

	fmt.Println("==> hop-agent service installed and started.")
	fmt.Printf("    Plist:  %s\n", plistPath)
	fmt.Println("    Logs:   /var/log/hop-agent.log")
}

func runAgentUninstall(args []string) {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	purge := fs.Bool("purge", false, "Also remove config directory (/etc/hop-agent/)")
	fs.Parse(args)

	switch runtime.GOOS {
	case "linux":
		uninstallAgentSystemd()
	case "darwin":
		uninstallAgentLaunchd()
	default:
		fmt.Fprintf(os.Stderr, "Error: Unsupported operating system: %s\n", runtime.GOOS)
		os.Exit(1)
	}

	if *purge {
		if err := os.RemoveAll(agentConfigDir); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Could not remove %s: %v\n", agentConfigDir, err)
		} else {
			fmt.Printf("    Removed: %s\n", agentConfigDir)
		}
	}
}

func uninstallAgentSystemd() {
	exec.Command("systemctl", "stop", agentServiceName).Run()
	exec.Command("systemctl", "disable", agentServiceName).Run()
	os.Remove(agentSystemdPath)
	exec.Command("systemctl", "daemon-reload").Run()
	fmt.Println("==> hop-agent service uninstalled.")
}

func uninstallAgentLaunchd() {
	home, _ := os.UserHomeDir()
	plistPath := filepath.Join(home, agentLaunchdPath)
	exec.Command("launchctl", "unload", plistPath).Run()
	os.Remove(plistPath)
	fmt.Println("==> hop-agent service uninstalled.")
}
