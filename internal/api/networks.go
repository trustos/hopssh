package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/trustos/hopssh/internal/auth"
	"github.com/trustos/hopssh/internal/db"
	"github.com/trustos/hopssh/internal/pki"
)

const caDuration = 10 * 365 * 24 * time.Hour // 10 years

// NetworkHandler manages network CRUD and node enrollment.
type NetworkHandler struct {
	Networks *db.NetworkStore
	Nodes    *db.NodeStore
}

func (h *NetworkHandler) CreateNetwork(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	slug := slugify(body.Name)

	// Generate Nebula CA.
	ca, err := pki.GenerateCA("hopssh-"+slug, caDuration)
	if err != nil {
		http.Error(w, "failed to generate CA: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Allocate subnet.
	subnet, err := h.Networks.AllocateSubnet()
	if err != nil {
		http.Error(w, "failed to allocate subnet: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Issue server cert (.1 in subnet).
	serverIP, err := pki.ServerAddress(subnet)
	if err != nil {
		http.Error(w, "failed to compute server IP: "+err.Error(), http.StatusInternalServerError)
		return
	}
	serverCert, err := pki.IssueCert(ca.CertPEM, ca.KeyPEM, "hopssh-server", serverIP, []string{"server"}, caDuration)
	if err != nil {
		http.Error(w, "failed to issue server cert: "+err.Error(), http.StatusInternalServerError)
		return
	}

	network := &db.Network{
		ID:           uuid.New().String(),
		UserID:       user.ID,
		Name:         body.Name,
		Slug:         slug,
		NebulaCACert: ca.CertPEM,
		NebulaCAKey:  ca.KeyPEM,
		NebulaSubnet: subnet,
		ServerCert:   serverCert.CertPEM,
		ServerKey:    serverCert.KeyPEM,
	}
	if err := h.Networks.Create(network); err != nil {
		http.Error(w, "failed to create network: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":     network.ID,
		"name":   network.Name,
		"slug":   network.Slug,
		"subnet": network.NebulaSubnet,
	})
}

func (h *NetworkHandler) ListNetworks(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	networks, err := h.Networks.ListForUser(user.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type networkEntry struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Slug      string `json:"slug"`
		Subnet    string `json:"subnet"`
		NodeCount int    `json:"nodeCount"`
		CreatedAt int64  `json:"createdAt"`
	}

	result := make([]networkEntry, 0, len(networks))
	for _, n := range networks {
		count, _ := h.Nodes.CountForNetwork(n.ID)
		result = append(result, networkEntry{
			ID:        n.ID,
			Name:      n.Name,
			Slug:      n.Slug,
			Subnet:    n.NebulaSubnet,
			NodeCount: count,
			CreatedAt: n.CreatedAt,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *NetworkHandler) GetNetwork(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")

	network, err := h.Networks.Get(networkID)
	if err != nil || network == nil || network.UserID != user.ID {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}

	nodes, _ := h.Nodes.ListForNetwork(networkID)
	count := len(nodes)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":        network.ID,
		"name":      network.Name,
		"slug":      network.Slug,
		"subnet":    network.NebulaSubnet,
		"nodeCount": count,
		"nodes":     nodes,
		"createdAt": network.CreatedAt,
	})
}

func (h *NetworkHandler) DeleteNetwork(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")

	network, err := h.Networks.Get(networkID)
	if err != nil || network == nil || network.UserID != user.ID {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}

	h.Nodes.DeleteForNetwork(networkID)
	if err := h.Networks.Delete(networkID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

var slugRe = regexp.MustCompile(`[^a-z0-9-]+`)

func slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "network"
	}
	return s
}
