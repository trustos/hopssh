//go:build !windows

package client

import (
	"os"
	"syscall"
)

// statFileOwner returns (uid, gid, true) for a file's owning user/group,
// or (0, 0, false) on stat failure or platform without Stat_t.
//
// Phase Z (v0.10.91): the chown self-heal logic in migrate.go uses
// this helper to identify (a) the mirror dir's owner as a fallback
// chown target when /dev/console isn't logged in yet, and (b) whether
// existing mirror files are still root-owned and need re-chowning.
//
// Platform-gated because syscall.Stat_t doesn't exist on Windows.
// Hopssh's mirror files are macOS-only by design (system-mode
// LaunchDaemon writes to ~/Library/Application Support/), so the
// Windows stub returning (0, 0, false) is a no-op-by-construction.
func statFileOwner(path string) (uid, gid uint32, ok bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return st.Uid, st.Gid, true
}
