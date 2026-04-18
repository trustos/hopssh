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

// NetworkEventsHandler serves the persistent activity log for a network.
type NetworkEventsHandler struct {
	Events   *db.NetworkEventStore
	Networks *db.NetworkStore
	Members  *db.NetworkMemberStore
}

// networkEventDTO is the JSON shape returned by the history endpoint.
type networkEventDTO struct {
	ID             int64   `json:"id"`
	NetworkID      string  `json:"networkId"`
	Type           string  `json:"type"`
	TargetID       *string `json:"targetId,omitempty"`
	TargetHostname *string `json:"targetHostname,omitempty"`
	Status         *string `json:"status,omitempty"`
	Details        *string `json:"details,omitempty"`
	CreatedAt      int64   `json:"createdAt"`
}

// networkEventHistoryLimitDefault bounds the default response size.
// Aligned with the frontend's typical page size.
const networkEventHistoryLimitDefault = 100

// networkEventHistoryLimitMax caps pathological requests.
const networkEventHistoryLimitMax = 1000

// networkEventHistoryDefaultWindow is the default "since" window when
// the caller omits the parameter — last 24 hours.
const networkEventHistoryDefaultWindow = 24 * time.Hour

// ListHistory serves GET /api/networks/{networkID}/events/history.
// Query params:
//   - since: unix seconds cutoff (default: now - 24h)
//   - type:  event type filter (exact match; empty = all)
//   - limit: max entries to return (default 100, max 1000)
func (h *NetworkEventsHandler) ListHistory(w http.ResponseWriter, r *http.Request) {
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

	q := r.URL.Query()
	since := time.Now().Add(-networkEventHistoryDefaultWindow).Unix()
	if s := q.Get("since"); s != "" {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil && v >= 0 {
			since = v
		}
	}

	eventType := q.Get("type")

	limit := networkEventHistoryLimitDefault
	if l := q.Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > networkEventHistoryLimitMax {
		limit = networkEventHistoryLimitMax
	}

	events, err := h.Events.ListForNetwork(networkID, since, eventType, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	out := make([]networkEventDTO, 0, len(events))
	for _, e := range events {
		out = append(out, networkEventDTO{
			ID:             e.ID,
			NetworkID:      e.NetworkID,
			Type:           e.EventType,
			TargetID:       e.TargetID,
			TargetHostname: e.TargetHostname,
			Status:         e.Status,
			Details:        e.Details,
			CreatedAt:      e.CreatedAt,
		})
	}
	writeJSON(w, out)
}
