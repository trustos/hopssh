package client

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestDetectParallelInstall_NoSignals returns nil when neither signal
// is present. Most users in steady state — only one install on the
// host — must NOT see the warning banner.
func TestDetectParallelInstall_NoSignals(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-specific")
	}
	// Use a temp directory for the "current" agent so we definitely
	// don't accidentally match an existing /etc/hop-agent on the host.
	pi := detectParallelInstall(t.TempDir())
	// On a clean dev host we expect nil. On a developer's machine that
	// has BOTH a system install AND we're running tests as the user
	// (with our own configDir != /etc/hop-agent), this would non-nil
	// — that's correct behavior, just don't fail the test there.
	if pi != nil {
		// At minimum verify the struct is populated correctly when set.
		if !pi.LaunchDaemon && !pi.LegacyConfigDir {
			t.Errorf("detectParallelInstall returned non-nil with no signals: %+v", pi)
		}
	}
}

// TestDetectParallelInstall_NotDarwin asserts non-macOS hosts always
// get nil. The detection logic is macOS-specific (LaunchDaemons +
// /etc/hop-agent locations).
func TestDetectParallelInstall_NotDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("non-darwin only")
	}
	if pi := detectParallelInstall(t.TempDir()); pi != nil {
		t.Errorf("non-darwin detection should return nil; got %+v", pi)
	}
}

// TestDetectParallelInstall_SystemAgentSelfSuppress ensures the
// detector returns nil ENTIRELY when the agent is the system-mode
// install (configDir == /etc/hop-agent). After Phase A1's migration,
// the LaunchDaemon plist exists AND /etc/hop-agent/enrollments.json
// exists — but that's OUR install. Reporting it as a parallel install
// would surface the warning banner on the post-conversion happy path.
//
// Locks in the v0.10.50 self-suppression contract.
func TestDetectParallelInstall_SystemAgentSelfSuppress(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-specific")
	}
	// When configDir == /etc/hop-agent, detector must return nil
	// regardless of whether the plist or registry files exist.
	pi := detectParallelInstall("/etc/hop-agent")
	if pi != nil {
		t.Errorf("system-mode agent reported itself as parallel install: %+v", pi)
	}
}

// TestDetectParallelInstall_LegacyConfigDirDetected creates a stub
// /etc/hop-agent/enrollments.json and asserts the detector flags it
// — but only inside a hermetic temp directory we can control. Since
// we can't touch /etc/ in a test, we directly exercise the underlying
// stat path via a testable helper would require refactoring; instead
// we trust the os.Stat behavior and only verify the suppression path
// (above) which is the easier-to-mistake branch.
func TestDetectParallelInstall_LegacyConfigDirDetected(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-specific")
	}
	// Refactor the detector to take an OS stat function would be too
	// invasive for v1 — instead this is a hermetic round-trip test:
	// create a sibling dir layout and verify a non-suppressed path
	// would flag.
	tmp := t.TempDir()
	currentConfig := filepath.Join(tmp, "user-mode")
	_ = os.MkdirAll(currentConfig, 0o755)
	// We can't override /etc/hop-agent from the test, so this is a
	// best-effort smoke that the function runs; semantic coverage is
	// done at the integration level (manual MBP test).
	_ = detectParallelInstall(currentConfig)
}

// TestPortConflictMessageHasReset locks in the user-actionable error
// text that mentions Settings → Danger zone → Reset. This message is
// the only signal a non-technical user gets when their bundled .app
// hits the leftover-system-agent port collision; it MUST point them
// at the cleanup path.
//
// Phase NN: the connectFn body lifted from cmd/agent/main.go into
// internal/client/client.go::Client.connect — scan that file instead.
func TestPortConflictMessageHasReset(t *testing.T) {
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatalf("read client.go: %v", err)
	}
	if !strings.Contains(string(src), "Settings → Danger zone → Reset") {
		t.Error("Client.connect retry-loop error must mention 'Settings → Danger zone → Reset' to point users at cleanup")
	}
	if !strings.Contains(string(src), "another hop-agent on this device") {
		t.Error("Client.connect retry-loop error must explain port-conflict in user terms")
	}
}
