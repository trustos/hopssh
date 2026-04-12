//go:build windows

package main

import "os"

// isPrivileged returns true if the process is likely running as Administrator.
// On Windows, os.Getuid() returns -1. We check if we can write to a
// system-protected path as a proxy for admin privileges.
func isPrivileged() bool {
	// Try creating a temp file in the Windows directory.
	f, err := os.CreateTemp(`C:\Windows`, "hop-priv-check-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}
