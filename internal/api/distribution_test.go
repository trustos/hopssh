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
