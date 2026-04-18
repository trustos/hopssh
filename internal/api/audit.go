package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/trustos/hopssh/internal/auth"
	"github.com/trustos/hopssh/internal/authz"
	"github.com/trustos/hopssh/internal/db"
)

// auditHistoryDefaultWindow matches the activity log history endpoint.
const auditHistoryDefaultWindow = 24 * time.Hour

// auditLimitDefault + auditLimitMax bound response sizes.
const (
	auditLimitDefault = 100
	auditLimitMax     = 1000
)

// parseAuditListParams reads the shared since/action/limit query params.
func parseAuditListParams(r *http.Request) (since int64, action string, limit int) {
	q := r.URL.Query()
	since = time.Now().Add(-auditHistoryDefaultWindow).Unix()
	if s := q.Get("since"); s != "" {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil && v >= 0 {
			since = v
		}
	}
	action = q.Get("action")
	limit = auditLimitDefault
	if l := q.Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > auditLimitMax {
		limit = auditLimitMax
	}
	return since, action, limit
}

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

	since, action, limit := parseAuditListParams(r)
	entries, err := h.Audit.ListForUser(user.ID, since, action, limit)
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

	since, action, limit := parseAuditListParams(r)
	entries, err := h.Audit.ListForNetwork(networkID, since, action, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, mapAuditEntries(entries))
}
