// Package authz provides authorization checks for resource access.
//
// Implements role-based access: network owners are "admin", invited users
// are "member". Handlers fetch membership alongside network, then call
// CheckAccess to determine the user's role.
package authz

import "github.com/trustos/hopssh/internal/db"

// Access represents a user's access level for a network.
type Access struct {
	Role string // "admin", "member", or "" (no access)
}

// CheckAccess determines a user's access level for a network.
// Owner always gets admin. Membership provides the stored role. Otherwise no access.
func CheckAccess(user *db.UserProfile, network *db.Network, membership *db.NetworkMember) Access {
	if user == nil || network == nil {
		return Access{}
	}
	if network.UserID == user.ID {
		return Access{Role: "admin"}
	}
	if membership != nil {
		return Access{Role: membership.Role}
	}
	return Access{}
}

// CanView returns true if the user has any access to the network.
func (a Access) CanView() bool { return a.Role != "" }

// CanAdmin returns true if the user has admin access to the network.
func (a Access) CanAdmin() bool { return a.Role == "admin" }

// CanAccessNetwork is a convenience for backward compatibility.
// Returns true if user owns the network OR has any membership.
func CanAccessNetwork(user *db.UserProfile, network *db.Network) bool {
	if user == nil || network == nil {
		return false
	}
	return network.UserID == user.ID
}
