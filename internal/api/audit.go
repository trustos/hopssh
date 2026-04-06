package api

import (
	"net/http"

	"github.com/trustos/hopssh/internal/auth"
	"github.com/trustos/hopssh/internal/db"
)

// AuditHandler serves the audit log.
type AuditHandler struct {
	Audit *db.AuditStore
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

	type auditEntry struct {
		ID        string  `json:"id"`
		UserID    string  `json:"userId"`
		NodeID    *string `json:"nodeId"`
		NetworkID *string `json:"networkId"`
		Action    string  `json:"action"`
		Details   *string `json:"details"`
		CreatedAt int64   `json:"createdAt"`
	}

	result := make([]auditEntry, 0, len(entries))
	for _, e := range entries {
		result = append(result, auditEntry{
			ID:        e.ID,
			UserID:    e.UserID,
			NodeID:    e.NodeID,
			NetworkID: e.NetworkID,
			Action:    e.Action,
			Details:   e.Details,
			CreatedAt: e.CreatedAt,
		})
	}
	writeJSON(w, result)
}
