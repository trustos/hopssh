// Package authz provides authorization checks for resource access.
//
// Currently implements a single-user ownership model where each network
// belongs to one user. When team features are added (Tier 2), this package
// is the only place that needs to change — handler code stays the same.
package authz

import "github.com/trustos/hopssh/internal/db"

// CanAccessNetwork returns true if the user is allowed to access the network.
// Currently checks direct ownership. Will be extended for team membership.
func CanAccessNetwork(user *db.UserProfile, network *db.Network) bool {
	if user == nil || network == nil {
		return false
	}
	return network.UserID == user.ID
}
