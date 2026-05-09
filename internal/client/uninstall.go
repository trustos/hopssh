package client

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// cleanupTarget is one file or directory the uninstall flow may remove.
// Targets are categorized so we can scope each pass:
//
//	categoryService — plist / systemd unit / SCM entry. Always removed.
//	categoryConfig  — enrollment registry, certs, on-disk state. --purge.
//	categoryLog     — log files. Kept by default (forensic). --remove-logs.
//	categoryBinary  — the hop-agent executable. Default + --remove-binary
//	                  for layered installs (curl-installed) where the
//	                  binary is part of what we own.
type cleanupTarget struct {
	Path     string
	Desc     string
	Category cleanupCategory
}

type cleanupCategory int

const (
	categoryService cleanupCategory = iota
	categoryConfig
	categoryLog
	categoryBinary
)

func (c cleanupCategory) String() string {
	switch c {
	case categoryService:
		return "service"
	case categoryConfig:
		return "config"
	case categoryLog:
		return "log"
	case categoryBinary:
		return "binary"
	}
	return "unknown"
}

// uninstallTargets returns every path the uninstall should consider on
// the current platform. Paths that don't exist are filtered out by
// removeTargets so the caller doesn't need to pre-check.
//
// IMPORTANT: this list is the source of truth for "what hopssh leaves
// behind on a host". Tests in uninstall_test.go assert each known
// leaver is enumerated; if you add a new install-time path elsewhere
// in the codebase, also add it here.
//
// We intentionally do NOT enumerate paths owned by the desktop client
// (~/Library/Application Support/com.hopssh.desktop, etc) — that's a
// different product (the Tauri shell), and macOS's drag-to-trash plus
// the desktop app's own teardown handles those. Mixing those into the
// agent uninstall would be a footgun for users who only want to remove
// the agent and keep the GUI client around.
func uninstallTargets() []cleanupTarget {
	switch runtime.GOOS {
	case "linux":
		return uninstallTargetsLinux()
	case "darwin":
		return uninstallTargetsDarwin()
	case "windows":
		return uninstallTargetsWindows()
	}
	return nil
}

func uninstallTargetsLinux() []cleanupTarget {
	t := []cleanupTarget{
		{Path: agentSystemdPath, Desc: "systemd unit", Category: categoryService},
		{Path: "/etc/systemd/resolved.conf.d/hopssh.conf", Desc: "systemd-resolved drop-in", Category: categoryService},

		{Path: "/etc/hop-agent", Desc: "system config dir", Category: categoryConfig},
	}
	if home, err := os.UserHomeDir(); err == nil {
		t = append(t,
			cleanupTarget{Path: filepath.Join(home, ".config", "hopssh"), Desc: "user config dir", Category: categoryConfig},
		)
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		t = append(t, cleanupTarget{Path: filepath.Join(xdg, "hopssh"), Desc: "XDG config dir", Category: categoryConfig})
	}
	t = append(t,
		cleanupTarget{Path: "/usr/local/bin/hop-agent", Desc: "agent binary", Category: categoryBinary},
		cleanupTarget{Path: "/usr/local/bin/hop", Desc: "CLI symlink", Category: categoryBinary},
	)
	return t
}

func uninstallTargetsDarwin() []cleanupTarget {
	t := []cleanupTarget{
		{Path: agentLaunchdDaemonPath, Desc: "system LaunchDaemon plist", Category: categoryService},

		{Path: "/etc/hop-agent", Desc: "system config dir (legacy)", Category: categoryConfig},
		{Path: "/var/hop-agent", Desc: "system config dir", Category: categoryConfig},

		{Path: "/var/log/hop-agent.log", Desc: "system stdout log", Category: categoryLog},
		{Path: "/var/log/hop-agent.err", Desc: "system stderr log", Category: categoryLog},
	}
	if home, err := os.UserHomeDir(); err == nil {
		t = append(t,
			cleanupTarget{Path: filepath.Join(home, "Library", "LaunchAgents", "com.hopssh.agent.plist"), Desc: "user LaunchAgent plist (agent)", Category: categoryService},
			// Phase AA (v0.10.92): the desktop client's autostart
			// LaunchAgent. Created by tauri-plugin-autostart in
			// Phase Y (v0.10.90) when the user enables "Open hopssh
			// on login" in Settings. The bundle id `com.hopssh.desktop`
			// matches `clients/desktop/src-tauri/tauri.conf.json::identifier`.
			// Without this entry, uninstalling hopssh leaves a stale
			// LaunchAgent that points at the deleted .app — launchd
			// fails silently every login until the user manually
			// removes the plist.
			cleanupTarget{Path: filepath.Join(home, "Library", "LaunchAgents", "com.hopssh.desktop.plist"), Desc: "user LaunchAgent plist (desktop autostart)", Category: categoryService},
			cleanupTarget{Path: filepath.Join(home, "Library", "Application Support", "hopssh"), Desc: "user config dir", Category: categoryConfig},
			cleanupTarget{Path: filepath.Join(home, "Library", "Logs", "hop-agent.log"), Desc: "user log", Category: categoryLog},
		)
	}
	t = append(t,
		cleanupTarget{Path: "/usr/local/bin/hop-agent", Desc: "agent binary", Category: categoryBinary},
		cleanupTarget{Path: "/usr/local/bin/hop", Desc: "CLI symlink", Category: categoryBinary},
	)
	return t
}

func uninstallTargetsWindows() []cleanupTarget {
	programData := os.Getenv("ProgramData")
	if programData == "" {
		programData = `C:\ProgramData`
	}
	t := []cleanupTarget{
		{Path: filepath.Join(programData, "hopssh"), Desc: "ProgramData logs", Category: categoryLog},
	}
	if home, err := os.UserHomeDir(); err == nil {
		t = append(t,
			cleanupTarget{Path: filepath.Join(home, ".config", "hopssh"), Desc: "user config dir", Category: categoryConfig},
		)
	}
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		t = append(t,
			cleanupTarget{Path: filepath.Join(pf, "hopssh", "hop-agent.exe"), Desc: "agent binary (ProgramFiles)", Category: categoryBinary},
			cleanupTarget{Path: filepath.Join(pf, "hopssh", "hop-agent.exe.old"), Desc: "stale self-update binary", Category: categoryBinary},
		)
	}
	return t
}

// runAgentUninstall is the entry point for `hop-agent uninstall`.
//
// Default behavior (apt-remove semantics): stop service, remove plists/
// units, remove the installed binary. Configs and logs are PRESERVED so
// a reinstall picks back up where you left off.
//
//	--purge          remove configs (enrollment registry, certs, state).
//	                 logs are still kept by default — they have forensic
//	                 value and are usually what you want post-mortem.
//	--remove-logs    remove log files (independent of --purge).
//	--remove-binary  remove /usr/local/bin/hop-agent. On by default; this
//	                 flag exists so --no-remove-binary can opt out for
//	                 distro-package installs that own the binary path.
//	--yes            skip the confirmation prompt for destructive ops.
//	--dry-run        print what would be removed without doing it.
//
// The output explicitly lists every path touched (or skipped) so an
// operator can see exactly what was wiped. Best-effort: if --purge is
// set, walk the enrollment registry and call `leave` on each before
// nuking certs, so each lighthouse gets a clean signal that this node
// is going away (rather than the server thinking it's just offline).
func RunAgentUninstall(args []string) {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	purge := fs.Bool("purge", false, "Also remove configs (enrollment registry, certs, state)")
	removeBinary := fs.Bool("remove-binary", true, "Remove the hop-agent binary (default: true; set --remove-binary=false to opt out)")
	removeLogs := fs.Bool("remove-logs", false, "Also remove log files (preserved by default for forensics)")
	yes := fs.Bool("yes", false, "Skip the confirmation prompt for destructive operations")
	dryRun := fs.Bool("dry-run", false, "List what would be removed without removing it")
	fs.Parse(args)

	all := uninstallTargets()

	// Build the planned cleanup list based on flag categories.
	categories := map[cleanupCategory]bool{categoryService: true}
	if *purge {
		categories[categoryConfig] = true
	}
	if *removeBinary {
		categories[categoryBinary] = true
	}
	if *removeLogs {
		categories[categoryLog] = true
	}

	var planned []cleanupTarget
	for _, t := range all {
		if !categories[t.Category] {
			continue
		}
		if _, err := os.Stat(t.Path); os.IsNotExist(err) {
			continue
		}
		planned = append(planned, t)
	}

	// Pre-flight: print exactly what will be removed BEFORE asking for
	// confirmation, so the user can see the full scope.
	printPreflight(planned, *purge, *removeBinary, *removeLogs, *dryRun)

	if *dryRun {
		return
	}

	// Confirmation prompt for destructive flags. --purge wipes certs
	// (you'd have to re-enroll); --remove-logs is forensic loss. The
	// service-only path is reversible (just reinstall) so no prompt
	// for that. --yes bypasses; non-TTY (pipe) auto-confirms with a
	// notice (so curl|sh install scripts can drive it).
	if needsConfirm(*purge, *removeLogs, *yes) {
		if !confirmPrompt() {
			fmt.Println("Cancelled.")
			return
		}
	}

	// If --purge, best-effort: notify each lighthouse that this node
	// is leaving BEFORE we delete the certs that the leave call needs
	// to authenticate. Done before stopping the service so the running
	// agent's network connectivity is still available.
	if *purge {
		bestEffortLeaveAll()
	}

	// Stop the service. Files held open by the running daemon (utun
	// fds, log files on Windows) won't be releasable until the process
	// exits, so this MUST come before file removal.
	stopServiceForUninstall()

	removeTargets(planned)

	printRemainingNotice(*purge, *removeBinary, *removeLogs)
}

// stopServiceForUninstall calls the platform-specific stop+unregister
// path. Idempotent: succeeds with a "(not installed)" message if no
// service is registered.
func stopServiceForUninstall() {
	switch runtime.GOOS {
	case "linux":
		uninstallAgentSystemd()
	case "darwin":
		uninstallAgentLaunchd()
	case "windows":
		uninstallAgentWindows()
	default:
		fmt.Fprintf(os.Stderr, "Note: service auto-uninstall not supported on %s\n", runtime.GOOS)
	}
}

// bestEffortLeaveAll walks the enrollment registry and tries to send a
// `leave` API call to each lighthouse so the server can mark the node
// gone. Errors are non-fatal — if the network is down or the lighthouse
// is unreachable, we still proceed with the local wipe. This is the
// "courtesy hangup" pattern (Tailscale's `tailscale logout`).
func bestEffortLeaveAll() {
	reg, err := loadEnrollmentRegistry(configDir)
	if err != nil || reg.Len() == 0 {
		return
	}
	fmt.Println()
	fmt.Println("Notifying lighthouses (best-effort):")
	for _, e := range reg.List() {
		// We don't have a server-side "delete me" endpoint yet (see
		// leave.go comment). For now, just print that the node will
		// appear offline post-uninstall — operators can prune from
		// the dashboard. Once the API endpoint exists, plug it in
		// here without changing the user-visible behavior.
		fmt.Printf("    %s @ %s — will appear offline; remove from the dashboard manually.\n", e.Name, e.Endpoint)
	}
}

// needsConfirm returns true if the destructive-action confirmation
// prompt should fire given the current flag combination. Extracted
// from runAgentUninstall so it can be unit-tested independently of
// the actual stdin reading.
//
// Critical for the desktop client's Tauri shell-out: the Tauri Reset
// (--purge --remove-binary=false --yes) and Uninstall (--purge
// --remove-binary --yes) commands MUST NOT trigger the prompt — there's
// no stdin attached to the osascript-spawned shell, so the agent would
// hang forever waiting for a y/N response. The --yes flag must always
// short-circuit the prompt regardless of which other destructive flags
// are present.
func needsConfirm(purge, removeLogs, yes bool) bool {
	if yes {
		return false
	}
	return purge || removeLogs
}

func confirmPrompt() bool {
	// Auto-confirm on non-TTY (pipe / curl|sh) so install scripts can
	// drive the uninstall non-interactively. Print a notice so the
	// user sees we made the choice for them.
	if fi, err := os.Stdin.Stat(); err == nil && (fi.Mode()&os.ModeCharDevice) == 0 {
		fmt.Println()
		fmt.Println("(non-interactive stdin; auto-confirming)")
		return true
	}
	fmt.Println()
	fmt.Print("Proceed with the operations above? [y/N] ")
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

func printPreflight(planned []cleanupTarget, purge, removeBinary, removeLogs, dryRun bool) {
	prefix := "The following will be removed:"
	if dryRun {
		prefix = "[dry-run] The following WOULD be removed:"
	}
	fmt.Println(prefix)
	if len(planned) == 0 {
		fmt.Println("    (nothing — no install artifacts found on this host)")
		return
	}
	for _, t := range planned {
		fmt.Printf("    %-10s %s — %s\n", "["+t.Category.String()+"]", t.Path, t.Desc)
	}

	// Always list what we are NOT touching so the user can see the
	// scope explicitly. This pre-empts "wait, did it remove my certs
	// too?" support questions.
	fmt.Println()
	fmt.Println("Preserved (not removed):")
	if !purge {
		fmt.Println("    [config]   enrollment registry + certs (use --purge to remove)")
	}
	if !removeLogs {
		fmt.Println("    [log]      log files (use --remove-logs to remove)")
	}
	if !removeBinary {
		fmt.Println("    [binary]   /usr/local/bin/hop-agent (default removed; --remove-binary=false opted out)")
	}
}

// removeTargets attempts to delete each target. Errors during remove
// (e.g. permission denied) are printed but don't abort; the user gets
// a full summary at the end.
func removeTargets(targets []cleanupTarget) {
	if len(targets) == 0 {
		return
	}
	fmt.Println()
	fmt.Println("Cleanup:")
	for _, t := range targets {
		if err := os.RemoveAll(t.Path); err != nil {
			fmt.Printf("    FAILED: %s — %v\n", t.Path, err)
			if isPermissionDenied(err) && os.Getuid() != 0 {
				fmt.Printf("            (re-run with sudo to remove root-owned paths)\n")
			}
			continue
		}
		fmt.Printf("    Removed: %s — %s\n", t.Path, t.Desc)
	}
}

func isPermissionDenied(err error) bool {
	return err != nil && (os.IsPermission(err) || strings.Contains(err.Error(), "permission denied"))
}

func printRemainingNotice(purged, removedBinary, removedLogs bool) {
	fmt.Println()
	if purged && removedBinary && removedLogs {
		fmt.Println("==> Full uninstall complete. Nothing hopssh-related remains.")
		return
	}
	fmt.Println("==> hop-agent uninstall complete.")
	if !purged {
		fmt.Println("    Configs preserved (you can reinstall and pick up where you left off).")
		fmt.Println("    To also remove configs:    hop-agent uninstall --purge")
	}
	if !removedLogs {
		fmt.Println("    Logs preserved (forensics).")
		fmt.Println("    To also remove logs:       hop-agent uninstall --remove-logs")
	}
	if !removedBinary {
		fmt.Println("    Binary preserved (--remove-binary=false).")
	}
}
