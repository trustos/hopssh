package authz

import (
	"testing"

	"github.com/trustos/hopssh/internal/db"
)

// CheckAccess + CanAccessNetwork + CanEnrollNode govern who can read,
// who can administer, and who can enroll devices into a given network.
// These predicates are tiny but security-critical — every wrong answer
// is either a privilege-escalation OR a legitimate-user lockout. The
// v0.10.85 colleague-onboarding bug was a CanAccessNetwork-too-strict
// instance of the second class. Lock the matrix in with a tripwire.

var (
	owner    = &db.UserProfile{ID: "user-owner"}
	memberA  = &db.UserProfile{ID: "user-member"}
	memberB  = &db.UserProfile{ID: "user-other-member"}
	stranger = &db.UserProfile{ID: "user-stranger"}

	network = &db.Network{ID: "net-1", UserID: owner.ID}

	memberRow = &db.NetworkMember{NetworkID: network.ID, UserID: memberA.ID, Role: "member"}
	adminRow  = &db.NetworkMember{NetworkID: network.ID, UserID: memberB.ID, Role: "admin"}
)

func TestCheckAccess_Owner(t *testing.T) {
	got := CheckAccess(owner, network, nil)
	if got.Role != "admin" {
		t.Errorf("owner Role = %q, want %q", got.Role, "admin")
	}
	if !got.CanView() || !got.CanAdmin() {
		t.Errorf("owner must CanView + CanAdmin")
	}
}

func TestCheckAccess_Member(t *testing.T) {
	got := CheckAccess(memberA, network, memberRow)
	if got.Role != "member" {
		t.Errorf("member Role = %q, want %q", got.Role, "member")
	}
	if !got.CanView() {
		t.Errorf("member must CanView")
	}
	if got.CanAdmin() {
		t.Errorf("member must NOT CanAdmin")
	}
}

func TestCheckAccess_AdminMember(t *testing.T) {
	got := CheckAccess(memberB, network, adminRow)
	if got.Role != "admin" {
		t.Errorf("admin-member Role = %q, want %q", got.Role, "admin")
	}
	if !got.CanView() || !got.CanAdmin() {
		t.Errorf("admin-member must CanView + CanAdmin")
	}
}

func TestCheckAccess_Stranger(t *testing.T) {
	got := CheckAccess(stranger, network, nil)
	if got.Role != "" {
		t.Errorf("stranger Role = %q, want empty", got.Role)
	}
	if got.CanView() || got.CanAdmin() {
		t.Errorf("stranger must NOT CanView/CanAdmin")
	}
}

func TestCheckAccess_NilSafe(t *testing.T) {
	// Defensive: handlers that fail to load user/network should not
	// panic; they must surface as no-access.
	for _, c := range []struct {
		name string
		u    *db.UserProfile
		n    *db.Network
	}{
		{"both nil", nil, nil},
		{"nil user", nil, network},
		{"nil network", owner, nil},
	} {
		got := CheckAccess(c.u, c.n, nil)
		if got.Role != "" {
			t.Errorf("%s: Role = %q, want empty", c.name, got.Role)
		}
	}
}

// TestCanAccessNetwork_OwnerOnly is the historical (pre-v0.10.85)
// predicate's contract — kept as the reference point. Owner passes,
// EVERYONE else (including legitimate members) fails.
func TestCanAccessNetwork_OwnerOnly(t *testing.T) {
	if !CanAccessNetwork(owner, network) {
		t.Error("owner must pass CanAccessNetwork")
	}
	if CanAccessNetwork(memberA, network) {
		t.Error("member must NOT pass CanAccessNetwork (owner-only by contract)")
	}
	if CanAccessNetwork(memberB, network) {
		t.Error("admin-member must NOT pass CanAccessNetwork (owner-only by contract)")
	}
	if CanAccessNetwork(stranger, network) {
		t.Error("stranger must NOT pass CanAccessNetwork")
	}
}

// TestCanEnrollNode_PermitsMembers is the core v0.10.85 fix. Owner,
// admin-member, and member all pass (any user with view access to a
// network can enroll their own devices into it). Stranger fails.
func TestCanEnrollNode_PermitsMembers(t *testing.T) {
	cases := []struct {
		name       string
		user       *db.UserProfile
		membership *db.NetworkMember
		want       bool
	}{
		{"owner", owner, nil, true},
		{"admin member", memberB, adminRow, true},
		{"member", memberA, memberRow, true},
		{"stranger", stranger, nil, false},
		{"member but membership row missing (race)", memberA, nil, false},
	}
	for _, c := range cases {
		got := CanEnrollNode(c.user, network, c.membership)
		if got != c.want {
			t.Errorf("%s: CanEnrollNode = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestCanEnrollNode_NilSafe — handlers that fail to load network/user
// must surface as deny, never as a panic.
func TestCanEnrollNode_NilSafe(t *testing.T) {
	if CanEnrollNode(nil, network, nil) {
		t.Error("nil user must deny")
	}
	if CanEnrollNode(owner, nil, nil) {
		t.Error("nil network must deny")
	}
	if CanEnrollNode(nil, nil, nil) {
		t.Error("both nil must deny")
	}
}
