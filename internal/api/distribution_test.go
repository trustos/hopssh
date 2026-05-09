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
		// macOS — Apple Silicon only since 2026-05-09.
		{"hopssh-macos-aarch64.dmg", true},

		// Windows — MSI + NSIS installers.
		{"hopssh-windows-x86_64.msi", true},
		{"hopssh-windows-aarch64.msi", true},
		{"hopssh-windows-x86_64-setup.exe", true},
		{"hopssh-windows-aarch64-setup.exe", true},

		// Linux — AppImage + .deb + .rpm. x86_64 only for v1.
		{"hopssh-linux-x86_64.AppImage", true},
		{"hopssh-linux-x86_64.deb", true},
		{"hopssh-linux-x86_64.rpm", true},

		// macOS Intel was dropped — must now be rejected.
		{"hopssh-macos-x86_64.dmg", false},

		// Path-traversal + injection attempts must be rejected.
		{"../../etc/passwd", false},
		{"hopssh-macos-aarch64.dmg/../foo", false},
		{"hopssh-macos-aarch64.exe", false},      // wrong ext for macos
		{"hopssh-windows-x86_64.dmg", false},     // wrong ext for windows
		{"hopssh-darwin-aarch64.dmg", false},     // wrong os name
		{"hopssh-macos-amd64.dmg", false},        // wrong arch encoding
		{"hopssh-macos-aarch64.dmg.sig", false},  // unsupported ext
		{"HOPSSH-MACOS-AARCH64.DMG", false},      // wrong case
		{"hopssh-linux-aarch64.AppImage", false}, // ARM64 Linux not built today
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

// Phase CC (v0.10.95) — pickNewerVersion is the helper used by /version
// to return max(current, fetched-from-GitHub). Eliminates the confusing
// "Latest available: v0.10.93" while running v0.10.94 window right
// after a tag bump.
func TestPickNewerVersion(t *testing.T) {
	cases := []struct {
		a, b, want string
		desc       string
	}{
		{"v0.10.94", "v0.10.93", "v0.10.94", "current newer than fetched"},
		{"v0.10.93", "v0.10.94", "v0.10.94", "fetched newer than current"},
		{"v0.10.94", "v0.10.94", "v0.10.94", "exact tie returns first"},
		{"v0.10.94-dirty", "v0.10.93", "v0.10.94-dirty", "dirty suffix preserved on returned string but stripped for compare"},
		{"v0.10.94-dirty", "v0.10.94", "v0.10.94-dirty", "tie on stripped components returns first arg"},
		{"v1.0.0", "v0.99.99", "v1.0.0", "major version dominance"},
		{"v0.10.10", "v0.10.9", "v0.10.10", "patch numeric not lexical"},
		// Parse-failure paths fall back to a (preserving caller's intent).
		{"v0.10.94", "garbage", "v0.10.94", "fetched parse failure → keep current"},
		{"garbage", "v0.10.93", "garbage", "current parse failure → return current as-is (don't fabricate newer)"},
		{"", "v0.10.93", "v0.10.93", "empty current → return fetched"},
		{"v0.10.94", "", "v0.10.94", "empty fetched → return current"},
	}
	for _, c := range cases {
		got := pickNewerVersion(c.a, c.b)
		if got != c.want {
			t.Errorf("%s: pickNewerVersion(%q, %q) = %q, want %q",
				c.desc, c.a, c.b, got, c.want)
		}
	}
}

// Phase CC — versionCacheTTL was reduced from 5min to 60s. Tripwire:
// no future "let's tune this" should silently bump it back to a value
// that defeats the freshness goal of Phase CC.
func TestVersionCacheTTLIsTight(t *testing.T) {
	// Anything > 5min is suspicious — that was the pre-Phase-CC value.
	if versionCacheTTL > 2*60*1_000_000_000 { // 2 minutes in nanoseconds
		t.Errorf("versionCacheTTL is %v, expected <= 2min for fresh /version responses post-tag-bump", versionCacheTTL)
	}
}
