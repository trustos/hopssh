package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/trustos/hopssh/internal/auth"
	"github.com/trustos/hopssh/internal/authz"
	"github.com/trustos/hopssh/internal/db"
)

// AuditHandler serves the audit log.
type AuditHandler struct {
	Audit    *db.AuditStore
	Networks *db.NetworkStore
	Members  *db.NetworkMemberStore
}

// auditEntry is the JSON response DTO for audit log entries.
type auditEntry struct {
	ID           string  `json:"id"`
	UserID       string  `json:"userId"`
	UserEmail    *string `json:"userEmail"`
	UserName     *string `json:"userName"`
	NodeID       *string `json:"nodeId"`
	NodeHostname *string `json:"nodeHostname"`
	NetworkID    *string `json:"networkId"`
	Action       string  `json:"action"`
	Details      *string `json:"details"`
	CreatedAt    int64   `json:"createdAt"`
}

func mapAuditEntries(entries []*db.AuditEntry) []auditEntry {
	result := make([]auditEntry, 0, len(entries))
	for _, e := range entries {
		result = append(result, auditEntry{
			ID:           e.ID,
			UserID:       e.UserID,
			UserEmail:    e.UserEmail,
			UserName:     e.UserName,
			NodeID:       e.NodeID,
			NodeHostname: e.NodeHostname,
			NetworkID:    e.NetworkID,
			Action:       e.Action,
			Details:      e.Details,
			CreatedAt:    e.CreatedAt,
		})
	}
	return result
}

// ListAuditLog returns recent audit entries for the authenticated user.
// @Summary      List audit log
// @Tags         audit
// @Security     BearerAuth
// @Produce      json
// @Success      200 {array} object
// @Router       /api/audit [get]
func (h *AuditHandler) ListAuditLog(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())

	entries, err := h.Audit.ListForUser(user.ID, 100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, mapAuditEntries(entries))
}

// ListNetworkAuditLog returns recent audit entries for a network (all members' actions).
// Requires at least view access to the network.
// @Summary      List network audit log
// @Tags         audit
// @Security     BearerAuth
// @Produce      json
// @Param        networkID path string true "Network ID"
// @Success      200 {array} object
// @Router       /api/networks/{networkID}/audit [get]
func (h *AuditHandler) ListNetworkAuditLog(w http.ResponseWriter, r *http.Request) {
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

	entries, err := h.Audit.ListForNetwork(networkID, 100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, mapAuditEntries(entries))
}
