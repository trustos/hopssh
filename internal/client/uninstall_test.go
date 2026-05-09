package client

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestUninstallTargetsCoverDarwin asserts that every macOS path the
// install flow OR the running agent leaves on disk is enumerated in
// uninstallTargetsDarwin. If a future change adds a new path (say,
// /etc/resolver/<domain> for per-domain DNS), THIS test must be
// updated alongside the new install code so `hop-agent uninstall
// --purge` stays a complete cleanup.
func TestUninstallTargetsCoverDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-specific")
	}
	must := []string{
		"/Library/LaunchDaemons/com.hopssh.agent.plist", // system service
		"/var/log/hop-agent.log",                        // system log path from launchd plist
		"/var/log/hop-agent.err",                        // system stderr (some launchd configs split)
		"/etc/hop-agent",                                // legacy system config dir
		"/var/hop-agent",                                // current system config dir
		"/usr/local/bin/hop-agent",                      // installed binary
	}
	if home, err := os.UserHomeDir(); err == nil {
		must = append(must,
			filepath.Join(home, "Library", "LaunchAgents", "com.hopssh.agent.plist"),
			// Phase AA (v0.10.92): the desktop's autostart LaunchAgent.
			// Created by tauri-plugin-autostart in Phase Y when the user
			// enables "Open hopssh on login". Must be in the uninstall
			// target list — without it, uninstalling hopssh leaves a
			// dangling LaunchAgent pointing at the deleted .app.
			filepath.Join(home, "Library", "LaunchAgents", "com.hopssh.desktop.plist"),
			filepath.Join(home, "Library", "Application Support", "hopssh"),
			filepath.Join(home, "Library", "Logs", "hop-agent.log"),
		)
	}
	assertTargetsContain(t, uninstallTargetsDarwin(), must)
}

// TestUninstallTargetsCoverLinux asserts each known leaver path on
// Linux is enumerated. systemd-resolved drop-in is included because
// dns_linux.go writes it on hosts where per-link DNS doesn't forward
// to non-53 ports (see CLAUDE.md Discovery Log entry).
func TestUninstallTargetsCoverLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-specific")
	}
	must := []string{
		"/etc/systemd/system/hop-agent.service",
		"/etc/systemd/resolved.conf.d/hopssh.conf",
		"/etc/hop-agent",
		"/usr/local/bin/hop-agent",
	}
	if home, err := os.UserHomeDir(); err == nil {
		must = append(must, filepath.Join(home, ".config", "hopssh"))
	}
	assertTargetsContain(t, uninstallTargetsLinux(), must)
}

// TestUninstallTargetsCoverWindows enumerates the Windows leavers.
// ProgramData hopssh dir holds the SCM-redirected log file (see
// service_windows.go::redirectLogsToFile).
func TestUninstallTargetsCoverWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-specific")
	}
	programData := os.Getenv("ProgramData")
	if programData == "" {
		programData = `C:\ProgramData`
	}
	must := []string{filepath.Join(programData, "hopssh")}
	if home, err := os.UserHomeDir(); err == nil {
		must = append(must, filepath.Join(home, ".config", "hopssh"))
	}
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		must = append(must,
			filepath.Join(pf, "hopssh", "hop-agent.exe"),
			filepath.Join(pf, "hopssh", "hop-agent.exe.old"),
		)
	}
	assertTargetsContain(t, uninstallTargetsWindows(), must)
}

// TestUninstallCategoryFilter verifies the per-platform target list
// has at least one entry in each category we expect (service, config,
// binary). Logs are platform-dependent (Linux uses journald + has no
// log file targets), so we don't assert log presence here.
func TestUninstallCategoryFilter(t *testing.T) {
	all := uninstallTargets()
	if len(all) == 0 {
		t.Skip("no targets for this platform")
	}

	count := func(targets []cleanupTarget, cat cleanupCategory) int {
		n := 0
		for _, tt := range targets {
			if tt.Category == cat {
				n++
			}
		}
		return n
	}

	if count(all, categoryService) == 0 {
		t.Errorf("expected at least one service target on %s", runtime.GOOS)
	}
	if count(all, categoryConfig) == 0 {
		t.Errorf("expected at least one config target on %s", runtime.GOOS)
	}
	if count(all, categoryBinary) == 0 {
		t.Errorf("expected at least one binary target on %s", runtime.GOOS)
	}
}

// TestUninstallLogsSeparateFromPurge is a tripwire that locks in the
// best-practice decision: logs are NOT removed by --purge alone. They
// have forensic value (postmortem of why a node went rogue, audit
// trails, support tickets) and matching `apt remove` / `apt purge`
// semantics, both leave /var/log alone. Anyone tempted to fold
// categoryLog into categoryConfig, see this test + the docstring on
// runAgentUninstall before doing it.
func TestUninstallLogsSeparateFromPurge(t *testing.T) {
	all := uninstallTargets()
	for _, tt := range all {
		// Anything that looks like a log file should be marked as
		// categoryLog, not categoryConfig.
		isLogPath := strings.Contains(tt.Path, "log") || strings.Contains(tt.Path, "Logs")
		if isLogPath && tt.Category == categoryConfig {
			t.Errorf("path %q looks like a log but is categorized as config — logs must be in categoryLog so they are not removed by --purge alone",
				tt.Path)
		}
	}
}

// TestUninstallTargetsExcludeDesktopAppBundle asserts the agent
// uninstall does NOT touch the Tauri .app bundle itself or its
// /Applications/ install location. Those are owned by macOS Finder
// + the GUI installer; pulling them on `hop-agent uninstall` would
// be a footgun for users who want to remove the daemon while
// keeping the .app for later reinstall.
//
// Phase AA (v0.10.92) intentionally ADDED
// `com.hopssh.desktop.plist` to the uninstall list — the desktop's
// autostart LaunchAgent (placed by tauri-plugin-autostart in
// Phase Y) IS something hopssh installs and therefore IS
// something a complete uninstall should remove. The pre-Phase-AA
// test forbade any path containing "com.hopssh.desktop"; that
// invariant is now wrong. The new invariant: forbid the .app
// bundle and the /Applications/ install location only.
func TestUninstallTargetsExcludeDesktopAppBundle(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-specific")
	}
	forbidden := []string{
		"hopssh.app",
		"/Applications/",
	}
	for _, tt := range uninstallTargetsDarwin() {
		for _, f := range forbidden {
			if strings.Contains(tt.Path, f) {
				t.Errorf("path %q matches forbidden substring %q — the agent uninstall must not touch the .app bundle or /Applications/ (those are owned by Finder + the GUI installer)",
					tt.Path, f)
			}
		}
	}
}

// TestUninstallYesBypassesPrompt locks in the contract that any
// flag-combo with --yes does NOT trigger the confirmation prompt.
//
// This is load-bearing for the desktop client's Tauri shell-out: the
// Reset and Uninstall buttons in Settings.svelte invoke the agent CLI
// via osascript "do shell script", which has no stdin attached. If
// the agent ever waits for a y/N response on those code paths, the
// admin prompt completes but the privileged child hangs forever and
// the user sees a stalled UI. Any future change to needsConfirm that
// breaks the --yes contract MUST fail this test.
func TestUninstallYesBypassesPrompt(t *testing.T) {
	cases := []struct {
		name            string
		purge, logs, yes bool
		want             bool
	}{
		// Default uninstall: no destructive scope, no prompt.
		{"defaults", false, false, false, false},
		// --purge alone: prompt.
		{"purge_no_yes", true, false, false, true},
		// --remove-logs alone: prompt.
		{"logs_no_yes", false, true, false, true},
		// --purge + --remove-logs: prompt.
		{"purge_and_logs_no_yes", true, true, false, true},

		// --yes MUST bypass the prompt for every destructive combo.
		// These are the desktop-client paths.
		{"purge_yes_reset_path", true, false, true, false},
		{"purge_yes_remove_logs_yes", true, true, true, false},
		{"logs_only_yes", false, true, true, false},
		{"purge_yes_uninstall_full_path", true, false, true, false},
	}
	for _, c := range cases {
		got := needsConfirm(c.purge, c.logs, c.yes)
		if got != c.want {
			t.Errorf("needsConfirm(purge=%v, logs=%v, yes=%v) = %v, want %v (case %q)",
				c.purge, c.logs, c.yes, got, c.want, c.name)
		}
	}
}

func assertTargetsContain(t *testing.T, targets []cleanupTarget, must []string) {
	t.Helper()
	enumerated := make(map[string]bool, len(targets))
	for _, tt := range targets {
		enumerated[tt.Path] = true
	}
	for _, want := range must {
		if !enumerated[want] {
			t.Errorf("uninstall targets missing %q (every install-time path must be enumerated for cleanup)", want)
		}
	}
}
