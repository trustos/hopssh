//go:build !windows

package main

// ensureWinTun is a no-op on non-Windows platforms.
func ensureWinTun() error {
	return nil
}
