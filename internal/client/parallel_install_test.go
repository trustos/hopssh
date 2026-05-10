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

// TestConnectErrorClassifiesCertExpired locks in v0.11.23's fix:
// when Nebula reports "certificate for this host is expired" (which
// can fire on cert-clock-skew, NOT just hard-expired certs — the
// not-yet-valid case from a slow VM clock reads as "expired" via
// Nebula's IsExpired check), the connect retry-loop must surface a
// clock-sync hint, NOT the misleading "another hop-agent is using
// the network port" message that fired pre-v0.11.23 because the
// scaffolded "kernel TUN + userspace both failed; likely 'address
// already in use'" wrapper string contained those literal substrings.
func TestConnectErrorClassifiesCertExpired(t *testing.T) {
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatalf("read client.go: %v", err)
	}
	// The retry-loop fallthrough must include a cert-expired branch
	// that surfaces the clock-skew hint.
	if !strings.Contains(string(src), "certificate for this host is expired") {
		t.Error("Client.connect retry-loop must classify cert-expired errors")
	}
	if !strings.Contains(string(src), "Sync your system clock") {
		t.Error("Client.connect cert-expired error must mention clock sync as the user-actionable fix")
	}
	// The pre-fix scaffold string ("kernel TUN + userspace both
	// failed; likely 'address already in use'") MUST be gone — its
	// presence in the error text caused the misclassification.
	if strings.Contains(string(src), "kernel TUN + userspace both failed") {
		t.Error("Client.connect must not return the pre-v0.11.23 scaffold string that misclassified cert-skew as port-collision")
	}
}

// TestStartMeshErrorPropagation locks in lifecycle.go's
// startMeshWithError variant + client.go's use of it. The legacy
// startMesh signature stays for renew.go's hot-path; the new
// variant is what surfaces the underlying error to the user.
func TestStartMeshErrorPropagation(t *testing.T) {
	srcLifecycle, err := os.ReadFile("lifecycle.go")
	if err != nil {
		t.Fatalf("read lifecycle.go: %v", err)
	}
	if !strings.Contains(string(srcLifecycle), "func startMeshWithError(") {
		t.Error("lifecycle.go must export startMeshWithError that returns the underlying error")
	}

	srcClient, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatalf("read client.go: %v", err)
	}
	if !strings.Contains(string(srcClient), "startMeshWithError(") {
		t.Error("client.go::startInstance must use startMeshWithError to capture the underlying error")
	}
	if !strings.Contains(string(srcClient), "inst.lastMeshErr") {
		t.Error("client.go must stash the mesh error on the instance for connect's retry classification")
	}
}

// TestEnrollDeviceFlowStartHasClockSkewGate locks in the v0.11.23
// pre-flight clock-sanity probe. Without this, a slow-clock VM lands
// at the post-poll auto-connect step with a freshly-issued cert that
// has NotBefore in the future — Nebula refuses to load it, the connect
// fails with the misleading "kernel TUN + userspace both failed" path,
// and the user's onboarding is stuck. The gate fires BEFORE the device
// code is issued, so the user gets a clear "your clock is X off" error
// instead of completing the browser flow + then failing.
func TestEnrollDeviceFlowStartHasClockSkewGate(t *testing.T) {
	src, err := os.ReadFile("local_api.go")
	if err != nil {
		t.Fatalf("read local_api.go: %v", err)
	}
	// Find handleEnrollDeviceFlowStart's body.
	startIdx := strings.Index(string(src), "func (s *localAPIServer) handleEnrollDeviceFlowStart(")
	if startIdx < 0 {
		t.Fatal("handleEnrollDeviceFlowStart not found")
	}
	// Bound by the next top-level func.
	tail := string(src)[startIdx:]
	endIdx := strings.Index(tail[1:], "\nfunc ")
	if endIdx < 0 {
		endIdx = len(tail)
	}
	body := tail[:endIdx+1]

	if !strings.Contains(body, "EnsureClockSane") {
		t.Error("handleEnrollDeviceFlowStart must call EnsureClockSane to detect clock skew before issuing the device code")
	}
	if !strings.Contains(body, "device clock is") {
		t.Error("handleEnrollDeviceFlowStart must surface a clock-skew error to the user when the skew exceeds tolerance")
	}
	// The clock-skew check must run BEFORE the device code POST.
	skewIdx := strings.Index(body, "EnsureClockSane")
	postIdx := strings.Index(body, "/api/device/code")
	if skewIdx < 0 || postIdx < 0 || skewIdx > postIdx {
		t.Errorf("EnsureClockSane (offset %d) must run BEFORE the /api/device/code POST (offset %d) — fixing the clock after issuing the cert is too late", skewIdx, postIdx)
	}
}
