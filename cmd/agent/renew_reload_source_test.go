package main

// Source-scan tripwire for the v0.10.33 cert-reload ordering invariant.
//
// The invariant tests in renew_reload_invariants_test.go cover behavior
// at runtime via fakes. This file adds a complementary STATIC check
// against the source of cmd/agent/renew.go::reloadNebula to catch the
// regression earlier — at compile-test time — without needing the
// runtime fake harness to fire.
//
// The bug v0.10.33 fixed: reloadNebula's hot-restart path called
// startNebulaByMode (the new svc start) BEFORE oldSvc.Close(), which
// always failed with "address already in use" because Nebula's UDP
// listen socket can't share-port. Any future refactor that re-orders
// these to put startNebulaByMode first re-introduces the bug. This
// test asserts the file's text reflects the correct order.

import (
	"os"
	"strings"
	"testing"
)

// TestReloadNebula_SourceOrder asserts that within the body of
// reloadNebula, the oldSvc.Close() call appears BEFORE the
// startNebulaByModeFn call in the source. This is a textual check, not
// a runtime check — its value is catching reorder-regressions during
// code review (the test fails on the next CI run if a refactor inverts
// the order, before any production agent encounters the cert-renewal
// failure that would surface the bug 24+ hours later).
func TestReloadNebula_SourceOrder(t *testing.T) {
	src, err := os.ReadFile("renew.go")
	if err != nil {
		t.Fatalf("read renew.go: %v", err)
	}
	body := string(src)

	// Locate the function header so we only scan within its body.
	fnStart := strings.Index(body, "func reloadNebula(inst *meshInstance)")
	if fnStart < 0 {
		t.Fatal("could not locate func reloadNebula in renew.go — has the function been renamed?")
	}
	// The next `\n}\n` after fnStart is the function's closing brace.
	bodyAfter := body[fnStart:]
	fnEnd := strings.Index(bodyAfter, "\n}\n")
	if fnEnd < 0 {
		t.Fatal("could not locate end of reloadNebula function body")
	}
	fnBody := bodyAfter[:fnEnd]

	// Find the hot-restart marker — the comment block that opens the
	// non-cold-start path. We scan AFTER this point so we don't pick up
	// the cold-start branch's startMesh call.
	hotMarker := strings.Index(fnBody, "Hot-restart")
	if hotMarker < 0 {
		t.Fatal("could not locate 'Hot-restart' marker in reloadNebula — comment removed?")
	}
	hotBody := fnBody[hotMarker:]

	closeIdx := strings.Index(hotBody, "oldSvc.Close()")
	startIdx := strings.Index(hotBody, "startNebulaByModeFn(")
	if closeIdx < 0 {
		t.Error("oldSvc.Close() not found in reloadNebula hot-restart body — close path removed?")
		return
	}
	if startIdx < 0 {
		t.Error("startNebulaByModeFn(...) not found in reloadNebula hot-restart body — has the call been moved?")
		return
	}
	if closeIdx > startIdx {
		t.Errorf(
			"reloadNebula ORDERING REGRESSION: oldSvc.Close() (offset %d) appears AFTER startNebulaByModeFn (offset %d).\n"+
				"Pre-v0.10.33 this exact ordering caused 'address already in use' on every cert-renewal hot-restart, leaving the agent on an OLD cert that peers eventually rejected as expired.\n"+
				"FIX: ensure oldSvc.Close() runs BEFORE startNebulaByModeFn in cmd/agent/renew.go::reloadNebula.",
			closeIdx, startIdx,
		)
	}

	// Belt-and-braces: confirm waitForUDPPortFreeFn is still called
	// between Close() and start. Without it, fast-path tests pass on
	// macOS but slower kernels (Linux under load) can leave the port
	// held briefly past Close() return.
	waitIdx := strings.Index(hotBody, "waitForUDPPortFreeFn(")
	if waitIdx < 0 {
		t.Error("waitForUDPPortFreeFn(...) not found in reloadNebula hot-restart body — port-release wait was removed; v0.10.33 fix is incomplete on slower kernels")
		return
	}
	if waitIdx < closeIdx || waitIdx > startIdx {
		t.Errorf(
			"reloadNebula ORDERING REGRESSION: waitForUDPPortFreeFn (offset %d) is NOT between Close() (%d) and start (%d).\n"+
				"Required order: Close() → waitForUDPPortFreeFn() → startNebulaByModeFn().",
			waitIdx, closeIdx, startIdx,
		)
	}

	// Confirm `inst.svc = nil` happens in the hot-restart body BEFORE
	// scheduleRetryReload is called. This is the precondition that
	// keeps the retry's haveSvc check from tripping as a false positive.
	svcNilIdx := strings.Index(hotBody, "inst.svc = nil")
	scheduleIdx := strings.Index(hotBody, "scheduleRetryReload(")
	if svcNilIdx < 0 {
		t.Error("inst.svc = nil not found in reloadNebula hot-restart body — invariant 'svc must be nil before retry is scheduled' may have been removed")
		return
	}
	if scheduleIdx < 0 {
		t.Error("scheduleRetryReload(...) not found — retry safety net removed?")
		return
	}
	if svcNilIdx > scheduleIdx {
		t.Errorf(
			"reloadNebula ORDERING REGRESSION: 'inst.svc = nil' (offset %d) appears AFTER scheduleRetryReload (offset %d).\n"+
				"Pre-v0.10.33 the retry goroutine's haveSvc check fired as a false positive on the lingering OLD svc, silently masking cert-reload failures. inst.svc MUST be nilled before scheduling.",
			svcNilIdx, scheduleIdx,
		)
	}
}
