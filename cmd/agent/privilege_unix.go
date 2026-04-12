//go:build !windows

package main

import "os"

// isPrivileged returns true if the process is running as root (UID 0).
func isPrivileged() bool {
	return os.Getuid() == 0
}
