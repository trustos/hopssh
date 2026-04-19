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
	agentServiceName      = "hop-agent"
	agentSystemdPath      = "/etc/systemd/system/hop-agent.service"
	agentLaunchdDaemonPath = "/Library/LaunchDaemons/com.hopssh.agent.plist"
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

	loadPrimaryEnrollment()

	// Verify enrollment has been completed.
	dir := activeEnrollDir()
	requiredFiles := []string{"token", "endpoint", "node-id", "ca.crt", "node.crt"}
	for _, f := range requiredFiles {
		path := filepath.Join(dir, f)
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
		if isPrivileged() {
			installAgentLaunchd()
		} else {
			installAgentLaunchdUser()
		}
	case "windows":
		installAgentWindows()
	default:
		fmt.Printf("  Service auto-install not supported on %s.\n", runtime.GOOS)
		fmt.Println("  Start manually: hop-agent serve")
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
	plistPath := agentLaunchdDaemonPath

	// Unload existing service if present (ignore errors).
	exec.Command("launchctl", "unload", plistPath).Run()

	if err := os.WriteFile(plistPath, []byte(agentLaunchdPlist), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error: Cannot write %s: %v\n", plistPath, err)
		fmt.Fprintf(os.Stderr, "Run with sudo: sudo hop-agent install\n")
		os.Exit(1)
	}

	if out, err := exec.Command("launchctl", "load", plistPath).CombinedOutput(); err != nil {
		log.Fatalf("Failed to load launchd service: %v\n%s", err, out)
	}

	fmt.Println("==> hop-agent service installed and started.")
	fmt.Printf("    Plist:  %s\n", plistPath)
	fmt.Println("    Logs:   /var/log/hop-agent.log")
	fmt.Println("    Stop:   sudo launchctl unload " + plistPath)
	fmt.Println("    Start:  sudo launchctl load " + plistPath)
}

func installAgentLaunchdUser() {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("Cannot determine home directory: %v", err)
	}
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	plistPath := filepath.Join(plistDir, "com.hopssh.agent.plist")

	if err := os.MkdirAll(plistDir, 0755); err != nil {
		log.Fatalf("Cannot create LaunchAgents directory: %v", err)
	}

	// Unload existing service if present.
	exec.Command("launchctl", "unload", plistPath).Run()

	// User-level plist runs as current user.
	binPath, _ := os.Executable()
	if binPath == "" {
		binPath = "/usr/local/bin/hop-agent"
	}
	logPath := filepath.Join(home, "Library", "Logs", "hop-agent.log")

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.hopssh.agent</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>serve</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, binPath, logPath, logPath)

	if err := os.WriteFile(plistPath, []byte(plist), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error: Cannot write %s: %v\n", plistPath, err)
		os.Exit(1)
	}

	if out, err := exec.Command("launchctl", "load", plistPath).CombinedOutput(); err != nil {
		log.Fatalf("Failed to load launchd service: %v\n%s", err, out)
	}

	fmt.Println("==> hop-agent service installed and started (user-level).")
	fmt.Printf("    Plist:  %s\n", plistPath)
	fmt.Printf("    Logs:   %s\n", logPath)
	fmt.Println("    Stop:   launchctl unload " + plistPath)
	fmt.Println("    Start:  launchctl load " + plistPath)
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
	case "windows":
		uninstallAgentWindows()
	default:
		fmt.Fprintf(os.Stderr, "Error: Unsupported operating system: %s\n", runtime.GOOS)
		os.Exit(1)
	}

	if *purge {
		if err := os.RemoveAll(configDir); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Could not remove %s: %v\n", configDir, err)
		} else {
			fmt.Printf("    Removed: %s\n", configDir)
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
	plistPath := agentLaunchdDaemonPath
	exec.Command("launchctl", "unload", plistPath).Run()
	os.Remove(plistPath)
	// Also clean up old LaunchAgents location if it exists.
	if home, err := os.UserHomeDir(); err == nil {
		oldPath := filepath.Join(home, "Library/LaunchAgents/com.hopssh.agent.plist")
		exec.Command("launchctl", "unload", oldPath).Run()
		os.Remove(oldPath)
	}
	fmt.Println("==> hop-agent service uninstalled.")
}

func runRestart() {
	switch runtime.GOOS {
	case "linux":
		if _, err := exec.LookPath("systemctl"); err == nil {
			if out, err := exec.Command("systemctl", "restart", agentServiceName).CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to restart: %v\n%s", err, out)
				os.Exit(1)
			}
			fmt.Println("==> hop-agent restarted.")
			return
		}
	case "darwin":
		for _, plist := range []string{
			agentLaunchdDaemonPath,
			filepath.Join(os.Getenv("HOME"), "Library/LaunchAgents/com.hopssh.agent.plist"),
		} {
			if _, err := os.Stat(plist); err == nil {
				exec.Command("launchctl", "unload", plist).Run()
				if out, err := exec.Command("launchctl", "load", plist).CombinedOutput(); err != nil {
					fmt.Fprintf(os.Stderr, "Failed to restart: %v\n%s", err, out)
					os.Exit(1)
				}
				fmt.Println("==> hop-agent restarted.")
				return
			}
		}
	case "windows":
		restartAgentWindows()
		return
	}
	fmt.Fprintf(os.Stderr, "No service found. Start manually: hop-agent serve\n")
	os.Exit(1)
}

func runStop() {
	switch runtime.GOOS {
	case "linux":
		if _, err := exec.LookPath("systemctl"); err == nil {
			exec.Command("systemctl", "stop", agentServiceName).Run()
			fmt.Println("==> hop-agent stopped.")
			return
		}
	case "darwin":
		for _, plist := range []string{
			agentLaunchdDaemonPath,
			filepath.Join(os.Getenv("HOME"), "Library/LaunchAgents/com.hopssh.agent.plist"),
		} {
			if _, err := os.Stat(plist); err == nil {
				exec.Command("launchctl", "unload", plist).Run()
				fmt.Println("==> hop-agent stopped.")
				return
			}
		}
	case "windows":
		stopAgentWindows()
		return
	}
	fmt.Fprintf(os.Stderr, "No service found.\n")
	os.Exit(1)
}
