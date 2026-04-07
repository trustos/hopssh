package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/trustos/hopssh/internal/auth"
	"github.com/trustos/hopssh/internal/authz"
	"github.com/trustos/hopssh/internal/mesh"
	"github.com/trustos/hopssh/internal/db"
	"github.com/trustos/hopssh/internal/pki"
)

const caDuration = 10 * 365 * 24 * time.Hour // 10 years

// NetworkHandler manages network CRUD and node enrollment.
type NetworkHandler struct {
	Networks       *db.NetworkStore
	Nodes          *db.NodeStore
	Members        *db.NetworkMemberStore
	NetworkManager *mesh.NetworkManager
	ForwardManager ForwardNetworkStopper // for cleaning up port forwards on network delete
}

// ForwardNetworkStopper stops all port forwards for a network.
type ForwardNetworkStopper interface {
	StopAllForNetwork(networkID string)
}

// CreateNetwork creates a new mesh network with auto-generated Nebula CA and subnet.
// @Summary      Create network
// @Description  Creates a new isolated mesh network. Auto-generates Nebula CA (Curve25519), allocates /24 subnet, and issues server certificate.
// @Tags         networks
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        body body CreateNetworkRequest true "Network name"
// @Success      201 {object} NetworkResponse
// @Failure      400 {object} ErrorResponse
// @Failure      401 {object} ErrorResponse
// @Router       /api/networks [post]
func (h *NetworkHandler) CreateNetwork(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var body struct {
		Name      string `json:"name"`
		DNSDomain string `json:"dnsDomain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	dnsDomain := body.DNSDomain
	if dnsDomain == "" {
		dnsDomain = "hop"
	}

	slug, err := uniqueSlug(slugify(body.Name), h.Networks)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

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
	// Server cert must expire before the CA. Subtract a buffer to avoid
	// the timing race where cert.NotAfter > ca.NotAfter by milliseconds.
	serverCertDuration := caDuration - time.Hour
	serverCert, err := pki.IssueCert(ca.CertPEM, ca.KeyPEM, "hopssh-server", serverIP, []string{"admin"}, serverCertDuration)
	if err != nil {
		http.Error(w, "failed to issue server cert: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Allocate a unique UDP port for this network's lighthouse.
	lighthousePort, err := h.NetworkManager.AllocatePort()
	if err != nil {
		http.Error(w, "failed to allocate lighthouse port: "+err.Error(), http.StatusInternalServerError)
		return
	}
	lhPort := int64(lighthousePort)

	network := &db.Network{
		ID:             uuid.New().String(),
		UserID:         user.ID,
		Name:           body.Name,
		Slug:           slug,
		NebulaCACert:   ca.CertPEM,
		NebulaCAKey:    ca.KeyPEM,
		NebulaSubnet:   subnet,
		ServerCert:     serverCert.CertPEM,
		ServerKey:      serverCert.KeyPEM,
		LighthousePort: &lhPort,
		DNSDomain:      dnsDomain,
	}
	if err := h.Networks.Create(network); err != nil {
		if db.IsUniqueViolation(err) {
			http.Error(w, "network name or subnet conflict, please try again", http.StatusConflict)
			return
		}
		http.Error(w, "failed to create network: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Add creator as admin member.
	if h.Members != nil {
		h.Members.Add(network.ID+"-owner", network.ID, user.ID, "admin")
	}

	// Start the lighthouse for this network.
	if err := h.NetworkManager.StartNetwork(network); err != nil {
		log.Printf("[networks] failed to start lighthouse for %s: %v", network.Slug, err)
		// Don't fail the request — network is created, lighthouse can be started later
	}

	writeJSONStatus(w, http.StatusCreated, map[string]interface{}{
		"id":             network.ID,
		"name":           network.Name,
		"slug":           network.Slug,
		"subnet":         network.NebulaSubnet,
		"lighthousePort": lighthousePort,
		"dnsDomain":      dnsDomain,
		"role":           "admin",
	})
}

// ListNetworks returns all networks for the authenticated user.
// @Summary      List networks
// @Description  Returns all mesh networks owned by the authenticated user with node counts.
// @Tags         networks
// @Security     BearerAuth
// @Produce      json
// @Success      200 {array} NetworkResponse
// @Failure      401 {object} ErrorResponse
// @Router       /api/networks [get]
func (h *NetworkHandler) ListNetworks(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	networks, err := h.Networks.ListForUser(user.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Build a set of owned network IDs for role determination.
	ownedIDs := make(map[string]bool, len(networks))
	for _, n := range networks {
		ownedIDs[n.ID] = true
	}

	// Also include networks where user is a member (not owner).
	if h.Members != nil {
		memberNetworkIDs, _ := h.Members.ListNetworkIDsForUser(user.ID)
		for _, nid := range memberNetworkIDs {
			if ownedIDs[nid] {
				continue // already in list as owner
			}
			n, err := h.Networks.Get(nid)
			if err != nil || n == nil {
				continue
			}
			networks = append(networks, n)
		}
	}

	type networkEntry struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		Slug           string `json:"slug"`
		Subnet         string `json:"subnet"`
		NodeCount      int    `json:"nodeCount"`
		LighthousePort *int64 `json:"lighthousePort"`
		DNSDomain      string `json:"dnsDomain"`
		Role           string `json:"role"`
		CreatedAt      int64  `json:"createdAt"`
	}

	result := make([]networkEntry, 0, len(networks))
	for _, n := range networks {
		count, _ := h.Nodes.CountForNetwork(n.ID)
		role := "member"
		if n.UserID == user.ID {
			role = "admin"
		}
		result = append(result, networkEntry{
			ID:             n.ID,
			Name:           n.Name,
			Slug:           n.Slug,
			Subnet:         n.NebulaSubnet,
			NodeCount:      count,
			LighthousePort: n.LighthousePort,
			DNSDomain:      n.DNSDomain,
			Role:           role,
			CreatedAt:      n.CreatedAt,
		})
	}

	writeJSON(w, result)
}

// GetNetwork returns a network's details including its nodes.
// @Summary      Get network
// @Description  Returns network details, subnet info, and all nodes.
// @Tags         networks
// @Security     BearerAuth
// @Produce      json
// @Param        networkID path string true "Network ID"
// @Success      200 {object} NetworkResponse
// @Failure      404 {object} ErrorResponse
// @Router       /api/networks/{networkID} [get]
func (h *NetworkHandler) GetNetwork(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")

	network, err := h.Networks.Get(networkID)
	if err != nil || network == nil {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}

	// Check access via ownership or membership.
	var membership *db.NetworkMember
	if h.Members != nil {
		membership, _ = h.Members.GetMembership(networkID, user.ID)
	}
	access := authz.CheckAccess(user, network, membership)
	if !access.CanView() {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}

	nodes, _ := h.Nodes.ListForNetwork(networkID)

	// Map to safe DTO — never expose AgentToken, EnrollmentToken, or keys.
	nodeResponses := make([]NodeResponse, 0, len(nodes))
	for _, n := range nodes {
		nodeResponses = append(nodeResponses, NodeResponse{
			ID:           n.ID,
			NetworkID:    n.NetworkID,
			Hostname:     n.Hostname,
			OS:           n.OS,
			Arch:         n.Arch,
			NebulaIP:     n.NebulaIP,
			AgentRealIP:  n.AgentRealIP,
			NodeType:     n.NodeType,
			ExposedPorts: n.ExposedPorts,
			DNSName:      n.DNSName,
			Status:       n.Status,
			LastSeenAt:   n.LastSeenAt,
			CreatedAt:    n.CreatedAt,
		})
	}

	writeJSON(w, map[string]interface{}{
		"id":             network.ID,
		"name":           network.Name,
		"slug":           network.Slug,
		"subnet":         network.NebulaSubnet,
		"nodeCount":      len(nodeResponses),
		"lighthousePort": network.LighthousePort,
		"dnsDomain":      network.DNSDomain,
		"role":           access.Role,
		"nodes":          nodeResponses,
		"createdAt":      network.CreatedAt,
	})
}

// DeleteNetwork removes a network and all its nodes.
// @Summary      Delete network
// @Description  Permanently deletes a network, all its nodes, and associated PKI material.
// @Tags         networks
// @Security     BearerAuth
// @Param        networkID path string true "Network ID"
// @Success      204
// @Failure      404 {object} ErrorResponse
// @Router       /api/networks/{networkID} [delete]
func (h *NetworkHandler) DeleteNetwork(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")

	network, err := h.Networks.Get(networkID)
	if err != nil || network == nil || !authz.CanAccessNetwork(user, network) {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}

	// Stop the lighthouse and port forwards for this network.
	if h.ForwardManager != nil {
		h.ForwardManager.StopAllForNetwork(networkID)
	}
	if h.NetworkManager != nil {
		h.NetworkManager.StopNetwork(networkID)
	}

	if err := h.Nodes.DeleteForNetwork(networkID); err != nil {
		log.Printf("[networks] failed to delete nodes for network %s: %v", networkID, err)
	}
	if err := h.Networks.Delete(networkID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

var slugRe = regexp.MustCompile(`[^a-z0-9-]+`)

func slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "network"
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// uniqueSlug appends -2, -3, etc. if the slug already exists.
func uniqueSlug(base string, networks *db.NetworkStore) (string, error) {
	slug := base
	for attempt := 2; attempt <= 100; attempt++ {
		if !networks.SlugExists(slug) {
			return slug, nil
		}
		slug = fmt.Sprintf("%s-%d", base, attempt)
	}
	return "", fmt.Errorf("could not generate unique name, please choose a different one")
}
