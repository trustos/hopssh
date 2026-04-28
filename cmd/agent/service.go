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

// buildLaunchdPlist returns the LaunchDaemon plist content. When
// mirrorDir is non-empty, the agent is launched with a `--mirror-dir`
// arg telling it to write the local-api token + port file into the
// console user's `~/Library/Application Support/hopssh/` directory.
// The desktop .app reads from there to attach to the system agent
// (see clients/desktop/src-tauri/src/agent.rs::try_attach_to_system_agent).
//
// When mirrorDir is empty (e.g. headless server install via
// `sudo hop-agent install`), no mirror is written — the agent runs
// purely as a CLI service.
func buildLaunchdPlist(mirrorDir string) string {
	args := []string{"/usr/local/bin/hop-agent", "serve"}
	if mirrorDir != "" {
		args = append(args, "--mirror-dir", mirrorDir)
	}
	var argsXML strings.Builder
	for _, a := range args {
		fmt.Fprintf(&argsXML, "    <string>%s</string>\n", a)
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.hopssh.agent</string>
  <key>ProgramArguments</key>
  <array>
%s  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ExitTimeOut</key>
  <integer>30</integer>
  <key>StandardOutPath</key>
  <string>/var/log/hop-agent.log</string>
  <key>StandardErrorPath</key>
  <string>/var/log/hop-agent.log</string>
</dict>
</plist>
`, argsXML.String())
}

func runAgentInstall(args []string) {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	migrateFrom := fs.String("migrate-from", "", "Move enrollments from this configDir into the system configDir before installing. Used by the desktop client to convert from bundled-mode to system-mode.")
	fs.Parse(args)

	// --migrate-from path: requires root, moves enrollments before
	// validating + installing. The migration validates structural
	// integrity up-front; if it fails, source files stay put + we
	// abort. See cmd/agent/migrate.go::migrateEnrollmentsToSystem.
	if *migrateFrom != "" {
		if !isPrivileged() {
			fmt.Fprintln(os.Stderr, "Error: --migrate-from requires root (run via sudo).")
			os.Exit(1)
		}
		// Force configDir to the system path. resolveConfigDir already
		// picks /etc/hop-agent for uid==0, but we set it explicitly so
		// the rest of this function operates on the post-migration dir.
		configDir = "/etc/hop-agent"
		if err := migrateEnrollmentsToSystem(*migrateFrom, configDir); err != nil {
			fmt.Fprintf(os.Stderr, "Error: migration failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("==> Migrated enrollments from %s -> %s\n", *migrateFrom, configDir)
	}

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

	// On macOS as root, build the launchd plist with a --mirror-dir arg
	// pointing at the console user's Application Support dir so the
	// desktop .app can read the agent's local-api token + port. Skip
	// when no console user is logged in (headless server scenario):
	// the agent runs CLI-only without surfacing to the .app.
	var mirrorDir string
	if runtime.GOOS == "darwin" && isPrivileged() {
		if user, home, err := resolveConsoleUser(); err == nil {
			mirrorDir = systemMirrorDir(home)
			// Pre-create the dir + chown to console user so the agent's
			// later token writes (running as root) can chown without
			// surprises.
			if err := os.MkdirAll(mirrorDir, 0o755); err == nil {
				_ = exec.Command("chown", user+":staff", mirrorDir).Run()
			}
		}
	}

	switch runtime.GOOS {
	case "linux":
		installAgentSystemd()
	case "darwin":
		if isPrivileged() {
			installAgentLaunchd(mirrorDir)
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

func installAgentLaunchd(mirrorDir string) {
	plistPath := agentLaunchdDaemonPath

	// Unload existing service if present (ignore errors).
	exec.Command("launchctl", "unload", plistPath).Run()

	plistContent := buildLaunchdPlist(mirrorDir)
	if err := os.WriteFile(plistPath, []byte(plistContent), 0644); err != nil {
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
	if mirrorDir != "" {
		fmt.Printf("    Mirror: %s (token + port for the desktop .app)\n", mirrorDir)
	}
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
  <key>ExitTimeOut</key>
  <integer>30</integer>
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

// uninstallAgentSystemd stops + disables the hop-agent systemd unit and
// removes the unit file. Idempotent — silent no-op if the unit isn't
// present. Called by the central uninstall flow in uninstall.go BEFORE
// file removal so any open file handles in the running daemon are
// released first.
func uninstallAgentSystemd() {
	if _, err := os.Stat(agentSystemdPath); os.IsNotExist(err) {
		fmt.Println("==> hop-agent systemd unit not installed (skipping stop).")
		return
	}
	exec.Command("systemctl", "stop", agentServiceName).Run()
	exec.Command("systemctl", "disable", agentServiceName).Run()
	exec.Command("systemctl", "daemon-reload").Run()
	fmt.Println("==> hop-agent systemd service stopped + disabled.")
}

// uninstallAgentLaunchd unloads BOTH the system LaunchDaemon and any
// user-level LaunchAgent plists, then leaves the plist files for the
// uninstall.go file-removal pass. macOS Sequoia's `launchctl unload`
// can return I/O errors when the daemon was already torn down; we
// swallow those so a "second-run" uninstall is silent.
func uninstallAgentLaunchd() {
	stopped := false
	if _, err := os.Stat(agentLaunchdDaemonPath); err == nil {
		exec.Command("launchctl", "bootout", "system", agentLaunchdDaemonPath).Run()
		exec.Command("launchctl", "unload", agentLaunchdDaemonPath).Run()
		stopped = true
	}
	if home, err := os.UserHomeDir(); err == nil {
		userPlist := filepath.Join(home, "Library", "LaunchAgents", "com.hopssh.agent.plist")
		if _, err := os.Stat(userPlist); err == nil {
			exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d", os.Getuid()), userPlist).Run()
			exec.Command("launchctl", "unload", userPlist).Run()
			stopped = true
		}
	}
	if stopped {
		fmt.Println("==> hop-agent launchd service stopped.")
	} else {
		fmt.Println("==> hop-agent launchd service not installed (skipping stop).")
	}
}

func runRestart(args []string) {
	fs := flag.NewFlagSet("restart", flag.ExitOnError)
	network := fs.String("network", "", "Informational: which enrollment prompted the restart (whole agent still restarts in v0.10.0)")
	fs.Parse(args)
	if n := strings.TrimSpace(*network); n != "" {
		fmt.Printf("Note: v0.10.0 restarts the whole agent; --network %s is informational only.\n", n)
	}

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
