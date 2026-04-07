package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/trustos/hopssh/internal/auth"
	"github.com/trustos/hopssh/internal/authz"
	"github.com/trustos/hopssh/internal/db"
)

type InviteHandler struct {
	Networks *db.NetworkStore
	Members  *db.NetworkMemberStore
	Invites  *db.InviteStore
}

// CreateInvite generates a new invite code for a network. Admin only.
// POST /api/networks/{networkID}/invites
func (h *InviteHandler) CreateInvite(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")

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

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		MaxUses   *int64 `json:"maxUses"`
		ExpiresIn *int64 `json:"expiresIn"` // seconds from now
		Role      string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	role := req.Role
	if role != "admin" {
		role = "member" // default
	}

	code := generateInviteCode()
	var expiresAt *int64
	if req.ExpiresIn != nil && *req.ExpiresIn > 0 {
		t := time.Now().Unix() + *req.ExpiresIn
		expiresAt = &t
	}

	invite := &db.NetworkInvite{
		ID:        uuid.New().String(),
		NetworkID: networkID,
		CreatedBy: user.ID,
		Code:      code,
		Role:      role,
		MaxUses:   req.MaxUses,
		ExpiresAt: expiresAt,
	}

	if err := h.Invites.Create(invite); err != nil {
		http.Error(w, "failed to create invite", http.StatusInternalServerError)
		return
	}

	writeJSONStatus(w, http.StatusCreated, map[string]interface{}{
		"id":        invite.ID,
		"code":      invite.Code,
		"role":      invite.Role,
		"maxUses":   invite.MaxUses,
		"expiresAt": invite.ExpiresAt,
		"createdAt": time.Now().Unix(),
	})
}

// ListInvites returns active invites for a network. Admin only.
// GET /api/networks/{networkID}/invites
func (h *InviteHandler) ListInvites(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")

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

	invites, err := h.Invites.ListForNetwork(networkID)
	if err != nil {
		http.Error(w, "failed to list invites", http.StatusInternalServerError)
		return
	}

	type inviteResponse struct {
		ID        string `json:"id"`
		Code      string `json:"code"`
		Role      string `json:"role"`
		MaxUses   *int64 `json:"maxUses"`
		UseCount  int64  `json:"useCount"`
		ExpiresAt *int64 `json:"expiresAt"`
		CreatedAt int64  `json:"createdAt"`
	}

	resp := make([]inviteResponse, len(invites))
	for i, inv := range invites {
		resp[i] = inviteResponse{
			ID:        inv.ID,
			Code:      inv.Code,
			Role:      inv.Role,
			MaxUses:   inv.MaxUses,
			UseCount:  inv.UseCount,
			ExpiresAt: inv.ExpiresAt,
			CreatedAt: inv.CreatedAt,
		}
	}
	writeJSON(w, resp)
}

// DeleteInvite revokes an invite. Admin only.
// DELETE /api/networks/{networkID}/invites/{inviteID}
func (h *InviteHandler) DeleteInvite(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")
	inviteID := chi.URLParam(r, "inviteID")

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

	if err := h.Invites.Delete(inviteID); err != nil {
		http.Error(w, "failed to delete invite", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetInviteByCode returns invite details for the accept page. Public.
// GET /api/invites/{code}
func (h *InviteHandler) GetInviteByCode(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")

	invite, err := h.Invites.GetByCode(code)
	if err != nil {
		http.Error(w, "invite not found", http.StatusNotFound)
		return
	}

	// Check expiry and usage.
	now := time.Now().Unix()
	if invite.ExpiresAt != nil && *invite.ExpiresAt < now {
		http.Error(w, "invite has expired", http.StatusGone)
		return
	}
	if invite.MaxUses != nil && invite.UseCount >= *invite.MaxUses {
		http.Error(w, "invite has reached its maximum uses", http.StatusGone)
		return
	}

	// Get network name for display.
	network, err := h.Networks.Get(invite.NetworkID)
	if err != nil || network == nil {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}

	writeJSON(w, map[string]interface{}{
		"code":        invite.Code,
		"networkId":   invite.NetworkID,
		"networkName": network.Name,
		"role":        invite.Role,
		"expiresAt":   invite.ExpiresAt,
	})
}

// AcceptInvite adds the authenticated user as a member of the network. Authenticated.
// POST /api/invites/{code}/accept
func (h *InviteHandler) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	code := chi.URLParam(r, "code")

	invite, err := h.Invites.Claim(code)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Check if already a member.
	existing, _ := h.Members.GetMembership(invite.NetworkID, user.ID)
	if existing != nil {
		http.Error(w, "already a member of this network", http.StatusConflict)
		return
	}

	memberID := uuid.New().String()
	if err := h.Members.Add(memberID, invite.NetworkID, user.ID, invite.Role); err != nil {
		if db.IsUniqueViolation(err) {
			http.Error(w, "already a member of this network", http.StatusConflict)
			return
		}
		http.Error(w, "failed to join network", http.StatusInternalServerError)
		return
	}

	writeJSONStatus(w, http.StatusCreated, map[string]interface{}{
		"networkId": invite.NetworkID,
		"role":      invite.Role,
	})
}

func generateInviteCode() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
