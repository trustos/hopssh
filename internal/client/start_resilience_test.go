package client

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// Phase GG (v0.11.26) tripwires. Cover the boot-resilience guarantee:
// a single bad enrollment must never bring down the entire agent
// process. Production evidence — yavortenev's MBP, 2026-06-01:
// work-enrollment cert expired → c.Start returned error → main.go
// log.Fatalf → process exit code 1 → launchd KeepAlive=true respawn →
// 92 restarts in 25 minutes (1 every ~15s), taking down healthy home +
// member-test enrollments along with the broken work one AND
// preventing client.StartLocalAPI from ever updating mirror files.
//
// These tests lock in the new contract by exercising the same path
// against fake connect functions.

func newTestClientForBootResilience(t *testing.T) (*Client, string) {
	t.Helper()
	dir := t.TempDir()
	c := &Client{
		cfg:       Config{ConfigDir: dir},
		enrolls:   &enrollmentRegistry{path: dir + "/enrollments.json"},
		instances: newInstanceRegistry(),
		servers:   newServerSet(),
		subChans:  map[uint64]chan Event{},
		subCBs:    map[uint64]EventCallback{},
	}
	return c, dir
}

// TestStart_OneEnrollmentFailsOthersSucceed locks in the load-bearing
// invariant: when N-1 of N enrollments succeed and 1 fails, Start
// returns nil (not error). Pre-Phase-GG this fail-fast behavior caused
// main.go's log.Fatalf to kill the entire agent process.
func TestStart_OneEnrollmentFailsOthersSucceed(t *testing.T) {
	c, _ := newTestClientForBootResilience(t)
	if err := c.enrolls.Add(&Enrollment{Name: "home", NodeID: "h"}); err != nil {
		t.Fatalf("seed home: %v", err)
	}
	if err := c.enrolls.Add(&Enrollment{Name: "work", NodeID: "w"}); err != nil {
		t.Fatalf("seed work: %v", err)
	}
	if err := c.enrolls.Add(&Enrollment{Name: "member-test", NodeID: "m"}); err != nil {
		t.Fatalf("seed member-test: %v", err)
	}

	c.connectOverride = func(name string) error {
		if name == "work" {
			return fmt.Errorf("nebula certificate for this host is expired")
		}
		return nil
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start should NOT return error when only some enrollments fail; got: %v", err)
	}

	// The failed enrollment's error must be surfaced via LastBootError.
	if got := c.LastBootError("work"); !strings.Contains(got, "certificate") {
		t.Errorf("LastBootError(\"work\") should contain 'certificate', got: %q", got)
	}
	if got := c.LastBootError("home"); got != "" {
		t.Errorf("LastBootError(\"home\") should be empty after successful connect, got: %q", got)
	}
	if got := c.LastBootError("member-test"); got != "" {
		t.Errorf("LastBootError(\"member-test\") should be empty after successful connect, got: %q", got)
	}
}

// TestStart_AllEnrollmentsFailReturnsError exercises the other half of
// the contract: when EVERY enrollment fails, Start returns error so
// main.go can log a single CRITICAL (without exiting — that's main's
// job). The error message must include the count so the operator
// understands the scope.
func TestStart_AllEnrollmentsFailReturnsError(t *testing.T) {
	c, _ := newTestClientForBootResilience(t)
	if err := c.enrolls.Add(&Enrollment{Name: "a", NodeID: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := c.enrolls.Add(&Enrollment{Name: "b", NodeID: "b"}); err != nil {
		t.Fatal(err)
	}
	c.connectOverride = func(name string) error {
		return fmt.Errorf("simulated failure for %s", name)
	}

	err := c.Start(context.Background())
	if err == nil {
		t.Fatal("Start should return error when ALL enrollments fail")
	}
	if !strings.Contains(err.Error(), "all") {
		t.Errorf("Start error should mention 'all'; got: %v", err)
	}
	if !strings.Contains(err.Error(), "2") {
		t.Errorf("Start error should mention enrollment count (2); got: %v", err)
	}
}

// TestStart_BootErrorClearedOnReconnectSuccess locks in the
// recordBootError / clearBootError pair. A successful Connect() after
// a failed Start() must clear the stale boot error so /local/status
// doesn't keep showing "work: cert expired" after the user re-enrolls.
func TestStart_BootErrorClearedOnReconnectSuccess(t *testing.T) {
	c, _ := newTestClientForBootResilience(t)
	if err := c.enrolls.Add(&Enrollment{Name: "work", NodeID: "w"}); err != nil {
		t.Fatal(err)
	}
	c.connectOverride = func(name string) error {
		return fmt.Errorf("cert expired")
	}
	_ = c.Start(context.Background())
	if got := c.LastBootError("work"); got == "" {
		t.Fatal("setup: bootError should be set after failed Start")
	}

	// Flip the override to succeed; call exported Connect.
	c.connectOverride = func(name string) error { return nil }
	if err := c.Connect(context.Background(), "work"); err != nil {
		t.Fatalf("Connect should succeed with stubbed override; got: %v", err)
	}
	if got := c.LastBootError("work"); got != "" {
		t.Errorf("LastBootError(\"work\") should be cleared after successful Connect; got: %q", got)
	}
}

// TestMain_StartFailureDoesNotCallFatalf is a source-scan tripwire
// against cmd/agent/main.go. The pre-Phase-GG line
//
//	if err := c.Start(shutdownCtx); err != nil {
//	    log.Fatalf("[agent] start: %v", err)
//	}
//
// was the death-spiral trigger. This test asserts that the Start error
// branch uses log.Printf (or any non-exiting logger), NOT log.Fatalf.
// Catches future refactors that re-introduce process-exit-on-Start.
func TestMain_StartFailureDoesNotCallFatalf(t *testing.T) {
	src, err := os.ReadFile("../../cmd/agent/main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	s := string(src)
	// Find the c.Start call site.
	idx := strings.Index(s, "c.Start(shutdownCtx)")
	if idx < 0 {
		t.Fatal("c.Start(shutdownCtx) call not found in main.go — refactor may have moved it; re-check this tripwire")
	}
	// Walk from the c.Start call to the matching closing brace of the
	// if-err block. Using brace-depth counting so we don't accidentally
	// catch later log.Fatalf calls (the debug-listener branch has one
	// legitimately — we don't want to flag that).
	window := s[idx:]
	depth := 0
	startedBlock := false
	end := len(window)
	for i, ch := range window {
		switch ch {
		case '{':
			depth++
			startedBlock = true
		case '}':
			depth--
			if startedBlock && depth == 0 {
				end = i + 1
				break
			}
		}
		if startedBlock && depth == 0 {
			end = i + 1
			break
		}
	}
	block := window[:end]

	if strings.Contains(block, "log.Fatalf") {
		t.Error("REGRESSION: cmd/agent/main.go calls log.Fatalf in the c.Start error branch. This caused the 2026-06-01 MBP death-spiral (92 launchd respawns in 25 min). Use log.Printf and let StartLocalAPI run regardless so users can fix things from the .app.")
	}
	if !strings.Contains(block, "log.Printf") {
		t.Error("cmd/agent/main.go's c.Start error branch must log something — without ANY log line, operators can't see what's wrong. Use log.Printf at minimum.")
	}
}
