package api

import "testing"

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
