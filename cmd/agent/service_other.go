//go:build !windows

package main

import "context"

// svcIntegrateIfNeeded is the non-Windows stub. On Unix the agent uses
// SIGINT/SIGTERM for graceful shutdown; no SCM integration needed.
func svcIntegrateIfNeeded(_ context.CancelFunc) bool { return false }

// cleanupOldBinary is a no-op on non-Windows. On Windows we leave a
// <exe>.old file behind after self-update and clean it up on next
// start — see service_windows.go.
func cleanupOldBinary() {}

// The four Windows-only helpers below are referenced unconditionally
// from service.go's runtime.GOOS switches. On !windows they're never
// called, but the compiler still needs them to resolve. Each panics
// so a bug that routed a non-Windows call here is caught loudly.
func installAgentWindows()   { panic("installAgentWindows called on non-Windows") }
func uninstallAgentWindows() { panic("uninstallAgentWindows called on non-Windows") }
func restartAgentWindows()   { panic("restartAgentWindows called on non-Windows") }
func stopAgentWindows()      { panic("stopAgentWindows called on non-Windows") }
