package main

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

// TestUninstallTargetsExcludeDesktopAppData asserts the agent uninstall
// does NOT touch the Tauri desktop client's data dirs. The .app is a
// separate product owned by macOS Finder + the GUI installer; pulling
// its data on `hop-agent uninstall` would be a footgun for users who
// only want to remove the daemon.
func TestUninstallTargetsExcludeDesktopAppData(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-specific")
	}
	forbidden := []string{
		"com.hopssh.desktop",
		"hopssh.app",
		"/Applications/",
	}
	for _, tt := range uninstallTargetsDarwin() {
		for _, f := range forbidden {
			if strings.Contains(tt.Path, f) {
				t.Errorf("path %q matches forbidden substring %q — the agent uninstall must not touch desktop-client paths",
					tt.Path, f)
			}
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
