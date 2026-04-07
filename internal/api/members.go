package api

import (
	"database/sql"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/trustos/hopssh/internal/auth"
	"github.com/trustos/hopssh/internal/authz"
	"github.com/trustos/hopssh/internal/db"
)

type MemberHandler struct {
	Networks *db.NetworkStore
	Members  *db.NetworkMemberStore
}

// ListMembers returns all members of a network.
// GET /api/networks/{networkID}/members
func (h *MemberHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")

	network, err := h.Networks.Get(networkID)
	if err != nil || network == nil {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}

	membership, _ := h.Members.GetMembership(networkID, user.ID)
	access := authz.CheckAccess(user, network, membership)
	if !access.CanView() {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}

	members, err := h.Members.ListForNetwork(networkID)
	if err != nil {
		http.Error(w, "failed to list members", http.StatusInternalServerError)
		return
	}

	type memberResponse struct {
		ID        string `json:"id"`
		UserID    string `json:"userId"`
		Email     string `json:"email"`
		Name      string `json:"name"`
		Role      string `json:"role"`
		CreatedAt int64  `json:"createdAt"`
	}

	resp := make([]memberResponse, len(members))
	for i, m := range members {
		resp[i] = memberResponse{
			ID:        m.ID,
			UserID:    m.UserID,
			Email:     m.Email,
			Name:      m.Name,
			Role:      m.Role,
			CreatedAt: m.CreatedAt,
		}
	}
	writeJSON(w, resp)
}

// RemoveMember removes a member from a network. Admin only.
// DELETE /api/networks/{networkID}/members/{memberID}
func (h *MemberHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")
	memberID := chi.URLParam(r, "memberID")

	network, err := h.Networks.Get(networkID)
	if err != nil || network == nil {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}

	membership, _ := h.Members.GetMembership(networkID, user.ID)
	access := authz.CheckAccess(user, network, membership)
	if !access.CanAdmin() {
		http.Error(w, "admin access required", http.StatusForbidden)
		return
	}

	if err := h.Members.Remove(memberID); err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "member not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to remove member", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
