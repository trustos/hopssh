package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// cleanupTarget is one file or directory the uninstall flow may remove.
// Targets are categorized so we can scope each pass: service plists
// always go (--no-flags); config + logs only on --purge; the binary
// only on --remove-binary.
type cleanupTarget struct {
	Path     string // absolute path
	Desc     string // user-visible label
	Category cleanupCategory
}

type cleanupCategory int

const (
	categoryService cleanupCategory = iota // plist / unit / SCM entry
	categoryConfig                         // config dir (certs, enrollment registry)
	categoryLog                            // log files
	categoryBinary                         // the hop-agent executable itself
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
// behind on a host". Tests assert each known leaver is enumerated; if
// you add a new install-time path elsewhere in the codebase, also add
// it here AND to TestUninstallTargetsCover* in uninstall_test.go.
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
			cleanupTarget{Path: filepath.Join(home, "Library", "LaunchAgents", "com.hopssh.agent.plist"), Desc: "user LaunchAgent plist", Category: categoryService},
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

// runAgentUninstall is the entry point for the `hop-agent uninstall`
// subcommand. The default behavior stops the service and removes the
// service-registration files (plist / systemd unit / SCM entry) — the
// minimum that "uninstalled" implies. Flags widen the scope:
//
//	--purge          also remove config dirs + log files
//	--remove-binary  also remove the hop-agent executable itself
//	                 (implies --purge — it's nonsensical to keep
//	                 configs around without a binary to use them)
//	--dry-run        print what would be removed without doing it
//
// The output explicitly lists every path touched (or skipped) so an
// operator can see exactly what was wiped, which makes troubleshooting
// half-uninstalls dramatically easier.
func runAgentUninstall(args []string) {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	purge := fs.Bool("purge", false, "Also remove config dirs + log files")
	removeBinary := fs.Bool("remove-binary", false, "Also remove the hop-agent binary (implies --purge)")
	dryRun := fs.Bool("dry-run", false, "List what would be removed without removing it")
	fs.Parse(args)

	if *removeBinary {
		*purge = true
	}

	all := uninstallTargets()

	// Step 1: stop the service. Files held open by the running daemon
	// (utun fds, log files on Windows) won't be releasable until the
	// process exits, so this MUST come before file removal.
	if !*dryRun {
		stopServiceForUninstall()
	} else {
		fmt.Println("[dry-run] would stop hop-agent service")
	}

	// Step 2: walk targets, applying the category filter.
	categories := map[cleanupCategory]bool{categoryService: true}
	if *purge {
		categories[categoryConfig] = true
		categories[categoryLog] = true
	}
	if *removeBinary {
		categories[categoryBinary] = true
	}

	var planned []cleanupTarget
	for _, t := range all {
		if !categories[t.Category] {
			continue
		}
		planned = append(planned, t)
	}

	removeTargets(planned, *dryRun)

	// Step 3: tell the user what's still left if they ran a partial
	// uninstall — saves a follow-up support email.
	printRemainingNotice(*purge, *removeBinary)
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

// removeTargets attempts to delete each target. Missing paths are
// reported as "(not present)" — no error. Errors during remove (e.g.
// permission denied) are printed but don't abort; the user gets a full
// summary at the end.
func removeTargets(targets []cleanupTarget, dryRun bool) {
	if len(targets) == 0 {
		return
	}
	fmt.Println()
	fmt.Println("Cleanup:")
	for _, t := range targets {
		if _, err := os.Stat(t.Path); os.IsNotExist(err) {
			fmt.Printf("    (skip) %s — %s (not present)\n", t.Path, t.Desc)
			continue
		}
		if dryRun {
			fmt.Printf("    [dry-run] would remove %s — %s\n", t.Path, t.Desc)
			continue
		}
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

func printRemainingNotice(purged, removedBinary bool) {
	if purged && removedBinary {
		fmt.Println()
		fmt.Println("==> Full uninstall complete. Nothing hopssh-related remains.")
		return
	}

	fmt.Println()
	fmt.Println("==> hop-agent uninstall complete.")
	if !purged {
		fmt.Println("    Config + logs preserved. To also remove them:")
		fmt.Println("        hop-agent uninstall --purge")
	}
	if !removedBinary {
		fmt.Println("    Binary at /usr/local/bin/hop-agent preserved. To also remove:")
		fmt.Println("        hop-agent uninstall --purge --remove-binary")
	}
}
