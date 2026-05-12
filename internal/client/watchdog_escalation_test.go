package client

import (
	"os"
	"strings"
	"testing"
)

// TestEscalationCallsOSExit75 locks in the centralized helper: after
// `escalateAfterN` consecutive failures, recordRestartFailure must call
// osExitFn(75) — the EX_TEMPFAIL convention that signals "respawn me"
// to launchd / systemd / SCM. Before N, it must NOT exit. Tests
// substitute a fake osExitFn so the test runner doesn't actually exit.
func TestEscalationCallsOSExit75(t *testing.T) {
	// Save + restore production defaults so other tests aren't poisoned.
	origExit := osExitFn
	origN := escalateAfterN
	defer func() {
		osExitFn = origExit
		escalateAfterN = origN
	}()

	var exitCode int
	var exitCalls int
	osExitFn = func(code int) {
		exitCode = code
		exitCalls++
	}
	escalateAfterN = 3

	var counter int

	// Failures 1 + 2 should NOT escalate.
	for i := 1; i <= 2; i++ {
		escalated := recordRestartFailure("watchdog", "home", &counter)
		if escalated {
			t.Errorf("failure %d: should NOT escalate (threshold is %d)", i, escalateAfterN)
		}
		if exitCalls != 0 {
			t.Errorf("failure %d: osExitFn called %d times (want 0)", i, exitCalls)
		}
		if counter != i {
			t.Errorf("failure %d: counter is %d, want %d", i, counter, i)
		}
	}

	// Failure 3 should escalate.
	escalated := recordRestartFailure("watchdog", "home", &counter)
	if !escalated {
		t.Fatalf("failure 3: should escalate (threshold %d, counter now %d)", escalateAfterN, counter)
	}
	if exitCalls != 1 {
		t.Errorf("failure 3: osExitFn called %d times (want 1)", exitCalls)
	}
	if exitCode != escalationExitCode {
		t.Errorf("failure 3: osExitFn called with code %d (want %d = EX_TEMPFAIL)", exitCode, escalationExitCode)
	}
	if escalationExitCode != 75 {
		t.Errorf("escalationExitCode is %d, want 75 (EX_TEMPFAIL from sysexits.h)", escalationExitCode)
	}
}

// TestEscalationResetsOnSuccess locks in the recordRestartSuccess reset:
// a transient wedge that restartFn fixes must NOT accumulate state
// across hours. Without this, a long-running agent with sporadic
// 1-trip wedges spread over weeks would eventually hit the N=3
// threshold even though each individual wedge recovered cleanly.
func TestEscalationResetsOnSuccess(t *testing.T) {
	origN := escalateAfterN
	defer func() { escalateAfterN = origN }()
	escalateAfterN = 3

	var counter int

	// Two failures...
	recordRestartFailure("watchdog", "home", &counter)
	recordRestartFailure("watchdog", "home", &counter)
	if counter != 2 {
		t.Fatalf("counter = %d, want 2", counter)
	}

	// ...then a success resets.
	recordRestartSuccess(&counter)
	if counter != 0 {
		t.Errorf("counter = %d after success, want 0 (reset)", counter)
	}

	// Two more failures should NOT escalate (we'd need 3 from zero).
	origExit := osExitFn
	defer func() { osExitFn = origExit }()
	var exitCalls int
	osExitFn = func(code int) { exitCalls++ }

	recordRestartFailure("watchdog", "home", &counter)
	recordRestartFailure("watchdog", "home", &counter)
	if exitCalls != 0 {
		t.Errorf("osExitFn called %d times after success-reset + 2 fails (want 0)", exitCalls)
	}
}

// TestEscalationHelperHasExpectedShape source-scans the escalation
// helper to confirm the load-bearing invariants stay in place across
// future refactors:
//   - escalationExitCode is 75 (EX_TEMPFAIL — the conventional
//     "respawn me" code that distinguishes from crash codes)
//   - osExitFn is a package-level var (injectable in tests)
//   - escalateAfterN is a package-level var (tunable in tests)
//   - recordRestartFailure calls osExitFn (the actual escalation path)
//   - recordRestartSuccess resets the counter to 0
func TestEscalationHelperHasExpectedShape(t *testing.T) {
	// Constants + threshold defaults.
	if escalationExitCode != 75 {
		t.Errorf("escalationExitCode = %d, want 75 (EX_TEMPFAIL)", escalationExitCode)
	}
	if escalateAfterN < 2 {
		t.Errorf("escalateAfterN = %d; below 2 means a single transient wedge triggers process exit — too aggressive", escalateAfterN)
	}

	// Source-scan: helper file must have the documented behaviors.
	src, err := readSourceFile("watchdog_escalation.go")
	if err != nil {
		t.Fatalf("read watchdog_escalation.go: %v", err)
	}
	if !strings.Contains(src, "osExitFn(escalationExitCode)") {
		t.Error("recordRestartFailure must call osExitFn(escalationExitCode); future refactors that change the call shape break the escalation chain silently")
	}
	if !strings.Contains(src, "*consecutive = 0") {
		t.Error("recordRestartSuccess must reset the counter to 0")
	}
}

// TestAllThreeWatchdogsWireEscalation source-scans each watchdog to
// confirm the recordRestartFailure / recordRestartSuccess pair is
// wired. Pre-fix the watchdogs logged "auto-restart failed" and moved
// on — no escalation. This tripwire catches regressions where someone
// removes the escalation wire (e.g., during a refactor that simplifies
// the error path).
func TestAllThreeWatchdogsWireEscalation(t *testing.T) {
	for _, f := range []string{
		"watcher_watchdog.go",
		"renew.go", // hosts runRenewalWatchdog
		"keepalive.go", // hosts watchdogTrip
	} {
		src, err := readSourceFile(f)
		if err != nil {
			t.Errorf("read %s: %v", f, err)
			continue
		}
		if !strings.Contains(src, "recordRestartFailure(") {
			t.Errorf("%s must call recordRestartFailure on restartFn failure — without this the watchdog will log and retry forever instead of escalating", f)
		}
		if !strings.Contains(src, "recordRestartSuccess(") {
			t.Errorf("%s must call recordRestartSuccess on restartFn success — without this the counter never resets and transient wedges over weeks would eventually escalate", f)
		}
	}
}

// readSourceFile is a thin helper used by multiple tripwires in this
// package. Centralizing it avoids each test re-implementing the
// boilerplate. Files live in the same directory as the test (the
// internal/client package source), so relative-path os.ReadFile works.
func readSourceFile(name string) (string, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
