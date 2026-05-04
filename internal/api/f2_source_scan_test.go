package api

import (
	"os"
	"strings"
	"testing"
)

// F2 (v0.10.85) prevents orphan node rows by short-circuiting with HTTP
// 409 when the agent indicates it already has the would-be network in
// its local registry. The check MUST run BEFORE any Nodes.Create or
// CompleteEnrollment call — otherwise the orphan exists by the time we
// reject. These source-scan tripwires lock the ordering.

func TestF2_Poll_ConflictBeforeNodesCreate(t *testing.T) {
	src, err := os.ReadFile("device.go")
	if err != nil {
		t.Fatalf("read device.go: %v", err)
	}
	body := string(src)

	idx := strings.Index(body, "func (h *DeviceHandler) Poll(")
	if idx < 0 {
		t.Fatal("Poll handler not found")
	}
	end := strings.Index(body[idx:], "\n}\n")
	if end < 0 {
		t.Fatal("end of Poll not found")
	}
	handler := body[idx : idx+end]

	if !strings.Contains(handler, "ExistingNetworkCAs") {
		t.Error("Poll request body must accept ExistingNetworkCAs (F2 client-supplied list)")
	}
	if !strings.Contains(handler, "caFingerprint(network.NebulaCACert)") {
		t.Error("Poll must compute caFingerprint(network.NebulaCACert) for the conflict check")
	}
	if !strings.Contains(handler, "http.StatusConflict") {
		t.Error("Poll must return http.StatusConflict (409) on duplicate network")
	}

	conflictIdx := strings.Index(handler, "http.StatusConflict")
	createIdx := strings.Index(handler, "h.Nodes.Create(")
	if conflictIdx < 0 || createIdx < 0 {
		t.Fatalf("expected both StatusConflict (%d) and Nodes.Create (%d) in handler", conflictIdx, createIdx)
	}
	if conflictIdx > createIdx {
		t.Errorf("ORDERING REGRESSION: F2 conflict short-circuit at offset %d appears AFTER Nodes.Create at offset %d — orphan still gets created",
			conflictIdx, createIdx)
	}
}

func TestF2_Enroll_ConflictBeforeCompleteEnrollment(t *testing.T) {
	src, err := os.ReadFile("enroll.go")
	if err != nil {
		t.Fatalf("read enroll.go: %v", err)
	}
	body := string(src)

	idx := strings.Index(body, "func (h *EnrollHandler) Enroll(")
	if idx < 0 {
		t.Fatal("Enroll handler not found")
	}
	end := strings.Index(body[idx:], "\n}\n")
	if end < 0 {
		t.Fatal("end of Enroll not found")
	}
	handler := body[idx : idx+end]

	if !strings.Contains(handler, "ExistingNetworkCAs") {
		t.Error("Enroll request body must accept ExistingNetworkCAs (F2 client-supplied list)")
	}
	if !strings.Contains(handler, "caFingerprint(network.NebulaCACert)") {
		t.Error("Enroll must compute caFingerprint(network.NebulaCACert) for the conflict check")
	}
	if !strings.Contains(handler, "http.StatusConflict") {
		t.Error("Enroll must return http.StatusConflict (409) on duplicate network")
	}

	conflictIdx := strings.Index(handler, "http.StatusConflict")
	completeIdx := strings.Index(handler, "h.Nodes.CompleteEnrollment(")
	if conflictIdx < 0 || completeIdx < 0 {
		t.Fatalf("expected both StatusConflict (%d) and CompleteEnrollment (%d) in handler", conflictIdx, completeIdx)
	}
	if conflictIdx > completeIdx {
		t.Errorf("ORDERING REGRESSION: F2 conflict short-circuit at offset %d appears AFTER CompleteEnrollment at offset %d — orphan still gets promoted",
			conflictIdx, completeIdx)
	}
}
