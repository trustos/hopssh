package api

import (
	"encoding/json"
	"net"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/trustos/hopssh/internal/auth"
	"github.com/trustos/hopssh/internal/authz"
	"github.com/trustos/hopssh/internal/db"
	"github.com/trustos/hopssh/internal/mesh"
)

var validDNSName = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

func isValidDNSName(name string) bool {
	return len(name) >= 1 && len(name) <= 63 && validDNSName.MatchString(name)
}

// DNSHandler manages DNS records for networks.
type DNSHandler struct {
	Networks       *db.NetworkStore
	DNSRecords     *db.DNSRecordStore
	Members        *db.NetworkMemberStore
	NetworkManager *mesh.NetworkManager
	Audit          *db.AuditStore
}

// ListDNSRecords returns all DNS records for a network.
// @Summary      List DNS records
// @Tags         dns
// @Security     BearerAuth
// @Produce      json
// @Param        networkID path string true "Network ID"
// @Success      200 {array} object
// @Router       /api/networks/{networkID}/dns [get]
func (h *DNSHandler) ListDNSRecords(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")

	network, err := h.Networks.Get(networkID)
	if err != nil || network == nil {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}

	// v0.10.89 (F6): DNS records describing a network are READ-ONLY
	// informational data — members of a network should be able to see
	// them. DNS *mutations* (Create/Delete below) need the owner or an
	// admin member (requireAdmin). Pre-fix this used CanAccessNetwork (owner-only),
	// which made the dashboard's network-detail page 404 for invited
	// members because /api/networks/{id}/dns is fetched in onMount.
	var membership *db.NetworkMember
	if h.Members != nil {
		membership, _ = h.Members.GetMembership(networkID, user.ID)
	}
	access := authz.CheckAccess(user, network, membership)
	if !access.CanView() {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}

	records, err := h.DNSRecords.ListForNetwork(networkID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Map to JSON-tagged DTO to ensure camelCase field names.
	type dnsEntry struct {
		ID        string `json:"id"`
		NetworkID string `json:"networkId"`
		Name      string `json:"name"`
		NebulaIP  string `json:"nebulaIP"`
		CreatedAt int64  `json:"createdAt"`
	}
	result := make([]dnsEntry, 0, len(records))
	for _, r := range records {
		result = append(result, dnsEntry{
			ID:        r.ID,
			NetworkID: r.NetworkID,
			Name:      r.Name,
			NebulaIP:  r.NebulaIP,
			CreatedAt: r.CreatedAt,
		})
	}
	writeJSON(w, result)
}

// CreateDNSRecord adds a custom DNS record to a network.
// @Summary      Create DNS record
// @Tags         dns
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        networkID path string true "Network ID"
// @Success      201 {object} object
// @Router       /api/networks/{networkID}/dns [post]
func (h *DNSHandler) CreateDNSRecord(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")

	if !h.requireAdmin(w, user, networkID) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var body struct {
		Name     string `json:"name"`
		NebulaIP string `json:"nebulaIP"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" || body.NebulaIP == "" {
		http.Error(w, "name and nebulaIP are required", http.StatusBadRequest)
		return
	}

	// Validate DNS name: alphanumeric + hyphens, 1-63 chars, no leading/trailing hyphen.
	if !isValidDNSName(body.Name) {
		http.Error(w, "invalid DNS name: must be 1-63 alphanumeric characters or hyphens", http.StatusBadRequest)
		return
	}

	// Validate IP address.
	if net.ParseIP(body.NebulaIP) == nil {
		http.Error(w, "invalid IP address", http.StatusBadRequest)
		return
	}

	if h.DNSRecords.NameExists(networkID, body.Name) {
		http.Error(w, "DNS record already exists", http.StatusConflict)
		return
	}

	id := uuid.New().String()
	if err := h.DNSRecords.Create(id, networkID, body.Name, body.NebulaIP); err != nil {
		http.Error(w, "failed to create DNS record: "+err.Error(), http.StatusInternalServerError)
		return
	}
	logAudit(h.Audit, user.ID, "dns.create", networkID, "", map[string]any{"recordId": id, "name": body.Name, "ip": body.NebulaIP})

	// Refresh DNS server with new records.
	if h.NetworkManager != nil {
		h.NetworkManager.RefreshDNS(networkID)
	}

	writeJSONStatus(w, http.StatusCreated, map[string]interface{}{
		"id":       id,
		"name":     body.Name,
		"nebulaIP": body.NebulaIP,
	})
}

// DeleteDNSRecord removes a DNS record.
// @Summary      Delete DNS record
// @Tags         dns
// @Security     BearerAuth
// @Param        networkID path string true "Network ID"
// @Param        recordID path string true "DNS Record ID"
// @Success      204
// @Router       /api/networks/{networkID}/dns/{recordID} [delete]
func (h *DNSHandler) DeleteDNSRecord(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")
	recordID := chi.URLParam(r, "recordID")

	if !h.requireAdmin(w, user, networkID) {
		return
	}

	// Read it first: the entry says what was deleted. A record that does not
	// exist is a 204 as before, with no entry; of two deletes at once only
	// the one that deleted it writes one.
	rec, err := h.DNSRecords.Get(recordID, networkID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	deleted, err := h.DNSRecords.Delete(recordID, networkID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if deleted {
		details := map[string]any{"recordId": recordID}
		if rec != nil {
			details["name"], details["ip"] = rec.Name, rec.NebulaIP
		}
		logAudit(h.Audit, user.ID, "dns.delete", networkID, "", details)
	}

	// Refresh DNS server.
	if h.NetworkManager != nil {
		h.NetworkManager.RefreshDNS(networkID)
	}

	w.WriteHeader(http.StatusNoContent)
}

// requireAdmin answers the request itself unless the user is the network's
// owner or an admin member: 404 to anyone who cannot see the network, 403
// to a plain member. DNS names are the network's policy, so a plain member
// may read them but not change them.
func (h *DNSHandler) requireAdmin(w http.ResponseWriter, user *db.UserProfile, networkID string) bool {
	network, err := h.Networks.Get(networkID)
	if err != nil || network == nil {
		http.Error(w, "network not found", http.StatusNotFound)
		return false
	}
	var membership *db.NetworkMember
	if h.Members != nil {
		membership, _ = h.Members.GetMembership(networkID, user.ID)
	}
	access := authz.CheckAccess(user, network, membership)
	if !access.CanView() {
		http.Error(w, "network not found", http.StatusNotFound)
		return false
	}
	if !access.CanAdmin() {
		http.Error(w, "admin access required", http.StatusForbidden)
		return false
	}
	return true
}
