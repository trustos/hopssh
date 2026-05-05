//go:build windows

package main

// statFileOwner is a no-op stub on Windows. The mirror-file chown
// path in migrate.go is macOS-only by design (LaunchDaemon writes to
// ~/Library/Application Support/, which doesn't exist on Windows);
// the chown self-heal goroutine short-circuits via os.Geteuid() != 0
// (Windows doesn't run as euid 0 in the same sense). Returning
// (0, 0, false) makes any caller skip the chown branch cleanly.
func statFileOwner(_ string) (uid, gid uint32, ok bool) {
	return 0, 0, false
}
