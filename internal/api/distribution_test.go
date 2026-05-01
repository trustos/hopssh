package api

import (
	"strings"
	"testing"
)

func TestValidDesktopAssetRegex(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"hopssh-macos-aarch64.dmg", true},
		{"hopssh-macos-x86_64.dmg", true},
		{"hopssh-windows-x86_64.exe", true},
		{"hopssh-windows-aarch64.exe", true},
		{"hopssh-linux-x86_64.AppImage", true},
		{"hopssh-linux-aarch64.AppImage", true},

		// Path-traversal + injection attempts must be rejected.
		{"../../etc/passwd", false},
		{"hopssh-macos-aarch64.dmg/../foo", false},
		{"hopssh-macos-aarch64.exe", false},      // wrong ext for macos
		{"hopssh-windows-x86_64.dmg", false},     // wrong ext for windows
		{"hopssh-darwin-aarch64.dmg", false},     // wrong os name
		{"hopssh-macos-amd64.dmg", false},        // wrong arch encoding
		{"hopssh-macos-aarch64.dmg.sig", false},  // unsupported ext
		{"HOPSSH-MACOS-AARCH64.DMG", false},      // wrong case
		{"", false},
		{"hopssh.dmg", false},
	}
	for _, c := range cases {
		got := validDesktopAsset.MatchString(c.name)
		if got != c.ok {
			t.Errorf("validDesktopAsset(%q) = %v, want %v", c.name, got, c.ok)
		}
	}
}

// Tripwire: the macOS installer must SIGKILL any running hopssh-desktop
// before replacing /Applications/hopssh.app, AND must flush
// SystemUIServer + ControlCenter afterward to clear the dead process's
// NSStatusItem ghost. Without the flush, the new install registers a
// fresh tray icon while the killed process's icon still sits in the
// SystemUIServer cache — user sees TWO icons, both opening the same
// window. Verified on user's Mac mini after a curl-install reproduced
// the duplicate-icon issue we previously fixed at the Tauri level.
//
// The flush MUST be conditional on a prior install existing — a clean
// first install has no stale registration to flush, and restarting
// SystemUIServer/ControlCenter blanks the user's whole menubar for ~3s
// (every other tray icon flashes) which is unjustified noise on a
// fresh-install run.
func TestMacInstallScript_FlushesGhostsOnReinstall(t *testing.T) {
	s := generateMacInstallScript("https://hopssh.com")

	if !strings.Contains(s, "killall hopssh-desktop") {
		t.Errorf("install script must SIGKILL any running hopssh-desktop before replacing /Applications/hopssh.app: %s", s)
	}
	if !strings.Contains(s, "killall SystemUIServer") {
		t.Errorf("install script must flush SystemUIServer to clear the killed hopssh-desktop's NSStatusItem ghost: %s", s)
	}
	if !strings.Contains(s, "killall ControlCenter") {
		t.Errorf("install script must flush ControlCenter alongside SystemUIServer (both cache status items): %s", s)
	}
	// The flush must be guarded behind a prior-install check so a
	// clean first install doesn't blank the menubar unnecessarily.
	if !strings.Contains(s, "HAD_PRIOR_INSTALL") {
		t.Errorf("install script must guard the menubar flush behind HAD_PRIOR_INSTALL — first install has no ghost to clear: %s", s)
	}

	// Order matters: kill hopssh-desktop FIRST (creates the ghost),
	// then replace the .app, then flush SystemUIServer (reaps the
	// ghost). A reordering would either fail to clear the ghost or
	// flush before the kill (no-op).
	killIdx := strings.Index(s, "killall hopssh-desktop")
	flushIdx := strings.Index(s, "killall SystemUIServer")
	if killIdx == -1 || flushIdx == -1 || flushIdx <= killIdx {
		t.Errorf("flush must come AFTER killall hopssh-desktop. killIdx=%d flushIdx=%d", killIdx, flushIdx)
	}
}

// Tripwire: install-mac.sh must request sudo UP FRONT so failures
// happen before the script has downloaded a 25 MB DMG and SIGKILL'd
// the running .app. Field evidence (May 2026): users repeatedly ran
// install-mac.sh without realizing sudo prompted mid-script (Touch
// ID timed out, terminal lost focus, etc.) — install silently
// aborted, the bundle stayed at the old version, and "Check for
// updates" kept reporting stale.
func TestMacInstallScript_PromptsSudoUpFront(t *testing.T) {
	s := generateMacInstallScript("https://hopssh.com")

	if !strings.Contains(s, "sudo -v") {
		t.Errorf("install script must call `sudo -v` up front to validate admin password BEFORE downloading the DMG. Without this, sudo prompts mid-script and silent failures abort the install: %s", s)
	}
	// The prompt must come before the download — otherwise we waste
	// the user's bandwidth / time on an install that can't complete.
	sudoIdx := strings.Index(s, "sudo -v")
	dlIdx := strings.Index(s, "==> Downloading")
	if sudoIdx == -1 || dlIdx == -1 || sudoIdx >= dlIdx {
		t.Errorf("sudo -v must come BEFORE the download. sudoIdx=%d dlIdx=%d", sudoIdx, dlIdx)
	}
}

// Tripwire (Phase J): install-mac.sh must refresh /usr/local/bin/hop-agent
// for system-mode users. The .app bundle's child binary gets
// replaced by `ditto`, but the LaunchDaemon at
// /Library/LaunchDaemons/com.hopssh.agent.plist runs the standalone
// /usr/local/bin/hop-agent which the .app updater doesn't touch.
// Without this step, system-mode users keep seeing the OLD agent
// version in Settings → Updates even after a successful install —
// a real bug observed on user's MBP at v0.10.66 across 6 release
// installs.
func TestMacInstallScript_RefreshesSystemAgentOnLaunchDaemon(t *testing.T) {
	s := generateMacInstallScript("https://hopssh.com")

	// Detection: must check for the LaunchDaemon plist before
	// touching /usr/local/bin/hop-agent (skip cleanly on bundled-mode
	// users who never enabled background mode).
	if !strings.Contains(s, "com.hopssh.agent.plist") {
		t.Errorf("install script must detect the system-mode LaunchDaemon before refreshing /usr/local/bin/hop-agent: %s", s)
	}
	// Refresh: copy the bundled binary to /usr/local/bin/hop-agent.
	if !strings.Contains(s, "/usr/local/bin/hop-agent") {
		t.Errorf("install script must refresh /usr/local/bin/hop-agent for system-mode users: %s", s)
	}
	// Reload: bouncing the LaunchDaemon makes the new binary
	// effective without a reboot.
	if !strings.Contains(s, "launchctl kickstart -k system/com.hopssh.agent") {
		t.Errorf("install script must kickstart the LaunchDaemon after replacing the binary so the new version is effective: %s", s)
	}
}
