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
	NetworkManager *mesh.NetworkManager
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
	if err != nil || network == nil || !authz.CanAccessNetwork(user, network) {
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

	network, err := h.Networks.Get(networkID)
	if err != nil || network == nil || !authz.CanAccessNetwork(user, network) {
		http.Error(w, "network not found", http.StatusNotFound)
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

	network, err := h.Networks.Get(networkID)
	if err != nil || network == nil || !authz.CanAccessNetwork(user, network) {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}

	if err := h.DNSRecords.Delete(recordID, networkID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Refresh DNS server.
	if h.NetworkManager != nil {
		h.NetworkManager.RefreshDNS(networkID)
	}

	w.WriteHeader(http.StatusNoContent)
}
