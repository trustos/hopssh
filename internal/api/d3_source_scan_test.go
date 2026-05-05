package api

import (
	"os"
	"strings"
	"testing"
)

// The v0.10.85 colleague-onboarding bug was caused by three handlers
// using authz.CanAccessNetwork (owner-only) where they should have
// permitted any member. The fix swaps each to CanEnrollNode after
// looking up membership. These source-scan tripwires assert (a) the
// load-bearing predicate is in place at each handler and (b) the
// membership lookup precedes the access check — so a future refactor
// can't silently re-introduce the bug.
//
// The empirical guarantee: post-fix, B (member of A's network) can POST
// /api/device/authorize with A's network ID and get HTTP 200 instead of
// HTTP 404 "network not found". That's verified end-to-end by the
// curl probe in the deploy smoke test (see scripts and the plan file).

func TestD3_DeviceAuthorize_UsesCanEnrollNode(t *testing.T) {
	src, err := os.ReadFile("device.go")
	if err != nil {
		t.Fatalf("read device.go: %v", err)
	}
	body := string(src)

	// Locate the Authorize handler.
	idx := strings.Index(body, "func (h *DeviceHandler) Authorize(")
	if idx < 0 {
		t.Fatal("Authorize handler not found in device.go")
	}
	end := strings.Index(body[idx:], "\n}\n")
	if end < 0 {
		t.Fatal("end of Authorize handler not found")
	}
	handler := body[idx : idx+end]

	if !strings.Contains(handler, "authz.CanEnrollNode(") {
		t.Error("Authorize must call authz.CanEnrollNode — found a different predicate (regression to owner-only?)")
	}
	if strings.Contains(handler, "authz.CanAccessNetwork(") {
		t.Error("Authorize still calls authz.CanAccessNetwork — that rejects members; use CanEnrollNode")
	}
	if !strings.Contains(handler, "h.Members.GetMembership(") {
		t.Error("Authorize must look up membership before calling CanEnrollNode")
	}

	membershipIdx := strings.Index(handler, "h.Members.GetMembership(")
	enrollIdx := strings.Index(handler, "authz.CanEnrollNode(")
	if membershipIdx > enrollIdx {
		t.Errorf("ORDERING REGRESSION: GetMembership at offset %d must come BEFORE CanEnrollNode at offset %d",
			membershipIdx, enrollIdx)
	}
}

func TestD3_CreateNode_UsesCanEnrollNode(t *testing.T) {
	assertCanEnrollNodeAtHandler(t, "enroll.go", "func (h *EnrollHandler) CreateNode(")
}

func TestD3_JoinNetwork_UsesCanEnrollNode(t *testing.T) {
	assertCanEnrollNodeAtHandler(t, "enroll.go", "func (h *EnrollHandler) JoinNetwork(")
}

func assertCanEnrollNodeAtHandler(t *testing.T, file, marker string) {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	body := string(src)
	idx := strings.Index(body, marker)
	if idx < 0 {
		t.Fatalf("handler %q not found in %s", marker, file)
	}
	end := strings.Index(body[idx:], "\n}\n")
	if end < 0 {
		t.Fatalf("end of %q not found", marker)
	}
	handler := body[idx : idx+end]

	if !strings.Contains(handler, "authz.CanEnrollNode(") {
		t.Errorf("%s must call authz.CanEnrollNode (regression to owner-only?)", marker)
	}
	if strings.Contains(handler, "authz.CanAccessNetwork(") {
		t.Errorf("%s still calls authz.CanAccessNetwork — rejects members; use CanEnrollNode", marker)
	}
	if !strings.Contains(handler, "h.Members.GetMembership(") {
		t.Errorf("%s must look up membership before calling CanEnrollNode", marker)
	}
	membershipIdx := strings.Index(handler, "h.Members.GetMembership(")
	enrollIdx := strings.Index(handler, "authz.CanEnrollNode(")
	if membershipIdx > enrollIdx {
		t.Errorf("%s ORDERING REGRESSION: GetMembership offset %d must precede CanEnrollNode offset %d",
			marker, membershipIdx, enrollIdx)
	}
}

// v0.10.89 (F6): ListDNSRecords MUST permit members — the dashboard's
// network-detail page fetches /api/networks/{id}/dns in onMount, and
// pre-fix the handler used CanAccessNetwork (owner-only) which made the
// whole page render "network not found" for invited members. The fix
// follows the same membership-aware predicate pattern as F3, except
// using CheckAccess.CanView() (read paths use CanView, enrollment-write
// paths use CanEnrollNode). DNS *mutations* (Create, Delete) intentionally
// stay owner-only — see TestD3_DNSCreateDelete_StayOwnerOnly below.
func TestD3_ListDNSRecords_PermitsMembers(t *testing.T) {
	src, err := os.ReadFile("dns.go")
	if err != nil {
		t.Fatalf("read dns.go: %v", err)
	}
	body := string(src)
	idx := strings.Index(body, "func (h *DNSHandler) ListDNSRecords(")
	if idx < 0 {
		t.Fatal("ListDNSRecords handler not found in dns.go")
	}
	end := strings.Index(body[idx:], "\n}\n")
	if end < 0 {
		t.Fatal("end of ListDNSRecords not found")
	}
	handler := body[idx : idx+end]

	if !strings.Contains(handler, "h.Members.GetMembership(") {
		t.Error("ListDNSRecords must look up membership for the access check")
	}
	if !strings.Contains(handler, "authz.CheckAccess(") {
		t.Error("ListDNSRecords must call authz.CheckAccess to reach CanView")
	}
	if !strings.Contains(handler, ".CanView()") {
		t.Error("ListDNSRecords must call .CanView() — members should see DNS records of networks they're a member of")
	}
	// The Networks.Get failure case keeps a `network == nil` 404 path
	// at the top BEFORE the membership lookup; we don't want to enforce
	// that the network-nil branch precedes the membership lookup.
	// What we DO want: the access check must fail (404) before any
	// further processing if CanView returns false.
	canViewIdx := strings.Index(handler, ".CanView()")
	listForNetworkIdx := strings.Index(handler, "h.DNSRecords.ListForNetwork(")
	if listForNetworkIdx < 0 {
		t.Fatal("ListDNSRecords must still call DNSRecords.ListForNetwork")
	}
	if canViewIdx > listForNetworkIdx {
		t.Errorf("ORDERING REGRESSION: CanView at offset %d must precede ListForNetwork at offset %d", canViewIdx, listForNetworkIdx)
	}
}

// TestD3_DNSCreateDelete_StayOwnerOnly: DNS mutations are privileged
// operational changes (DNS naming is per-network policy; members
// shouldn't be able to clobber the admin's records). Owner-only is
// the correct policy and must NOT widen.
func TestD3_DNSCreateDelete_StayOwnerOnly(t *testing.T) {
	src, err := os.ReadFile("dns.go")
	if err != nil {
		t.Fatalf("read dns.go: %v", err)
	}
	body := string(src)
	for _, marker := range []string{
		"func (h *DNSHandler) CreateDNSRecord(",
		"func (h *DNSHandler) DeleteDNSRecord(",
	} {
		idx := strings.Index(body, marker)
		if idx < 0 {
			// Not all handlers are present in every revision; skip
			// silently for handlers that don't exist.
			continue
		}
		end := strings.Index(body[idx:], "\n}\n")
		if end < 0 {
			t.Fatalf("end of %q not found", marker)
		}
		handler := body[idx : idx+end]
		if !strings.Contains(handler, "authz.CanAccessNetwork(") {
			t.Errorf("%s must use authz.CanAccessNetwork (owner-only) — DNS mutations are privileged", marker)
		}
		if strings.Contains(handler, "authz.CanEnrollNode(") {
			t.Errorf("%s must NOT use CanEnrollNode — would let members write DNS records", marker)
		}
	}
}

// TestD3_DeleteNetwork_StaysOwnerOnly belt-and-braces: the destructive
// DELETE /api/networks/{id} handler must NOT accidentally get widened
// to allow members. Owner-only is correct for destructive ops.
func TestD3_DeleteNetwork_StaysOwnerOnly(t *testing.T) {
	src, err := os.ReadFile("networks.go")
	if err != nil {
		t.Fatalf("read networks.go: %v", err)
	}
	body := string(src)
	idx := strings.Index(body, "func (h *NetworkHandler) DeleteNetwork(")
	if idx < 0 {
		t.Fatal("DeleteNetwork handler not found")
	}
	end := strings.Index(body[idx:], "\n}\n")
	if end < 0 {
		t.Fatal("end of DeleteNetwork not found")
	}
	handler := body[idx : idx+end]
	if !strings.Contains(handler, "authz.CanAccessNetwork(") {
		t.Error("DeleteNetwork must use authz.CanAccessNetwork (owner-only) — destructive op, do NOT widen to members")
	}
	if strings.Contains(handler, "authz.CanEnrollNode(") {
		t.Error("DeleteNetwork must NOT use CanEnrollNode — would let members delete owner's networks")
	}
}
