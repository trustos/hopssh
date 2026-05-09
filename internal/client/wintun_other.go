//go:build !windows

package client

// ensureWinTun is a no-op on non-Windows platforms.
func ensureWinTun() error {
	return nil
}
