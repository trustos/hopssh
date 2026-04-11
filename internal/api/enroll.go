package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/trustos/hopssh/internal/auth"
	"github.com/trustos/hopssh/internal/authz"
	"github.com/trustos/hopssh/internal/mesh"
	"github.com/trustos/hopssh/internal/db"
	"github.com/trustos/hopssh/internal/pki"
)

const nodeCertDuration = 24 * time.Hour // short-lived, auto-renewed by agent

// EnrollHandler manages node enrollment.
type EnrollHandler struct {
	Networks       *db.NetworkStore
	Nodes          *db.NodeStore
	NetworkManager *mesh.NetworkManager
	Endpoint       string // public URL of this server (e.g. "https://hopssh.com")
	LighthouseHost string // public IP/host for Nebula lighthouse UDP (separate from HTTP endpoint)
	EventHub       *EventHub
}

// CreateNode generates an enrollment token and returns the install command.
// @Summary      Add node to network
// @Description  Generates a one-time enrollment token for a new node. Returns the curl install command to run on the server.
// @Tags         nodes
// @Security     BearerAuth
// @Produce      json
// @Param        networkID path string true "Network ID"
// @Success      201 {object} CreateNodeResponse
// @Failure      404 {object} ErrorResponse "Network not found"
// @Router       /api/networks/{networkID}/nodes [post]
func (h *EnrollHandler) CreateNode(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")

	network, err := h.Networks.Get(networkID)
	if err != nil || network == nil || !authz.CanAccessNetwork(user, network) {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}

	// Find the next available Nebula IP using MAX to avoid collisions after deletes.
	nextIndex, err := h.Nodes.NextNodeIndex(networkID)
	if err != nil {
		http.Error(w, "failed to determine next node IP: "+err.Error(), http.StatusInternalServerError)
		return
	}
	nextIP, err := pki.NodeAddress(network.NebulaSubnet, nextIndex)
	if err != nil {
		http.Error(w, "failed to allocate node IP: "+err.Error(), http.StatusInternalServerError)
		return
	}

	enrollToken := generateToken()
	agentToken := generateToken()
	enrollExpiry := time.Now().Add(10 * time.Minute).Unix()

	node := &db.Node{
		ID:                  uuid.New().String(),
		NetworkID:           networkID,
		NebulaIP:            nextIP.String(),
		AgentToken:          agentToken,
		EnrollmentToken:     &enrollToken,
		EnrollmentExpiresAt: &enrollExpiry,
		Status:              "pending",
	}
	if err := h.Nodes.Create(node); err != nil {
		if db.IsUniqueViolation(err) {
			http.Error(w, "node IP conflict, please try again", http.StatusConflict)
			return
		}
		http.Error(w, "failed to create node: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSONStatus(w, http.StatusCreated, map[string]interface{}{
		"nodeId":          node.ID,
		"enrollmentToken": enrollToken,
		"endpoint":        h.Endpoint,
		"nebulaIP":        node.NebulaIP,
	})
}

// Enroll is called by the agent during installation. Public endpoint (no auth).
// @Summary      Agent enrollment
// @Description  Called by the install script. Validates the one-time enrollment token, issues a Nebula certificate, and returns connection details. The enrollment token is consumed and cannot be reused.
// @Tags         enrollment
// @Accept       json
// @Produce      json
// @Param        body body EnrollRequest true "Enrollment token and system info"
// @Success      200 {object} EnrollResponse
// @Failure      400 {object} ErrorResponse "Missing token"
// @Failure      401 {object} ErrorResponse "Invalid enrollment token"
// @Router       /api/enroll [post]
func (h *EnrollHandler) Enroll(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var body struct {
		Token    string `json:"token"`
		Hostname string `json:"hostname"`
		OS       string `json:"os"`
		Arch     string `json:"arch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token == "" {
		http.Error(w, "token is required", http.StatusBadRequest)
		return
	}

	node, err := h.Nodes.ClaimEnrollmentToken(body.Token)
	if err != nil || node == nil {
		http.Error(w, "invalid enrollment token", http.StatusUnauthorized)
		return
	}

	network, err := h.Networks.Get(node.NetworkID)
	if err != nil || network == nil {
		http.Error(w, "network not found", http.StatusInternalServerError)
		return
	}

	// Parse the node's pre-allocated Nebula IP directly.
	nodeIP, err := pki.ParsePrefix(node.NebulaIP)
	if err != nil {
		http.Error(w, "invalid node IP", http.StatusInternalServerError)
		return
	}

	// Issue node certificate.
	nodeCert, err := pki.IssueCert(network.NebulaCACert, network.NebulaCAKey,
		fmt.Sprintf("node-%s", node.ID[:8]), nodeIP, []string{"node"}, nodeCertDuration)
	if err != nil {
		http.Error(w, "failed to issue node cert: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Complete enrollment: store cert, consume token.
	if err := h.Nodes.CompleteEnrollment(node.ID, nodeCert.CertPEM, nodeCert.KeyPEM, body.Hostname, body.OS, body.Arch); err != nil {
		http.Error(w, "failed to complete enrollment: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Set DNS name from sanitized hostname.
	dnsName := sanitizeDNSName(body.Hostname)
	if err := h.Nodes.UpdateDNSName(node.ID, dnsName); err != nil {
		log.Printf("[enroll] failed to update DNS name for %s: %v", node.ID, err)
	}

	// Record the agent's real IP and refresh DNS.
	if err := h.Nodes.UpdateAgentRealIP(node.ID, captureAgentIP(r)); err != nil {
		log.Printf("[enroll] failed to update agent IP for %s: %v", node.ID, err)
	}
	if h.NetworkManager != nil {
		h.NetworkManager.RefreshDNS(node.NetworkID)
	}
	if h.EventHub != nil {
		h.EventHub.Publish(node.NetworkID, Event{Type: "node.enrolled", Data: map[string]string{"nodeId": node.ID, "hostname": body.Hostname}})
	}

	// Compute lighthouse VPN IP and port for agent's static_host_map.
	serverIP, _ := pki.ServerAddress(network.NebulaSubnet)
	lighthousePort := 0
	if network.LighthousePort != nil {
		lighthousePort = int(*network.LighthousePort)
	}

	resp := map[string]interface{}{
		"nodeId":         node.ID,
		"caCert":         string(network.NebulaCACert),
		"nodeCert":       string(nodeCert.CertPEM),
		"nodeKey":        string(nodeCert.KeyPEM),
		"agentToken":     node.AgentToken,
		"serverIP":       serverIP.Addr().String(),
		"nebulaIP":       node.NebulaIP,
		"lighthousePort": lighthousePort,
		"dnsDomain":      network.DNSDomain,
	}
	if h.LighthouseHost != "" {
		resp["lighthouseHost"] = h.LighthouseHost
	}
	writeJSON(w, resp)
}

// JoinNetwork allows a client device (laptop/phone) to join a mesh network.
// Authenticated endpoint — requires user session.
// @Summary      Join network as client
// @Description  Issues a "user" cert for a client device to join the mesh. Returns certs and lighthouse info.
// @Tags         enrollment
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        networkID path string true "Network ID"
// @Success      200 {object} EnrollResponse
// @Router       /api/networks/{networkID}/join [post]
func (h *EnrollHandler) JoinNetwork(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")

	network, err := h.Networks.Get(networkID)
	if err != nil || network == nil || !authz.CanAccessNetwork(user, network) {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var body struct {
		Hostname string `json:"hostname"`
		OS       string `json:"os"`
		Arch     string `json:"arch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	nextIndex, err := h.Nodes.NextNodeIndex(networkID)
	if err != nil {
		http.Error(w, "failed to allocate IP: "+err.Error(), http.StatusInternalServerError)
		return
	}
	nextIP, err := pki.NodeAddress(network.NebulaSubnet, nextIndex)
	if err != nil {
		http.Error(w, "failed to allocate IP: "+err.Error(), http.StatusInternalServerError)
		return
	}

	agentToken := generateToken()
	nodeID := uuid.New().String()

	node := &db.Node{
		ID:         nodeID,
		NetworkID:  networkID,
		Hostname:   body.Hostname,
		OS:         body.OS,
		Arch:       body.Arch,
		NebulaIP:   nextIP.String(),
		AgentToken: agentToken,
		NodeType:   "node",
		Status:     "enrolled",
	}
	if err := h.Nodes.Create(node); err != nil {
		http.Error(w, "failed to create node: "+err.Error(), http.StatusInternalServerError)
		return
	}

	nodeCert, err := pki.IssueCert(network.NebulaCACert, network.NebulaCAKey,
		fmt.Sprintf("node-%s", nodeID[:8]), nextIP, []string{"node"}, nodeCertDuration)
	if err != nil {
		http.Error(w, "failed to issue cert: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := h.Nodes.CompleteEnrollment(nodeID, nodeCert.CertPEM, nodeCert.KeyPEM, body.Hostname, body.OS, body.Arch); err != nil {
		http.Error(w, "failed to complete enrollment: "+err.Error(), http.StatusInternalServerError)
		return
	}

	dnsName := sanitizeDNSName(body.Hostname)
	h.Nodes.UpdateDNSName(nodeID, dnsName)
	if err := h.Nodes.UpdateAgentRealIP(nodeID, captureAgentIP(r)); err != nil {
		log.Printf("[enroll] failed to update agent IP for %s: %v", nodeID, err)
	}
	if h.NetworkManager != nil {
		h.NetworkManager.RefreshDNS(networkID)
	}

	serverIP, _ := pki.ServerAddress(network.NebulaSubnet)
	lighthousePort := 0
	if network.LighthousePort != nil {
		lighthousePort = int(*network.LighthousePort)
	}

	resp := map[string]interface{}{
		"nodeId":         nodeID,
		"caCert":         string(network.NebulaCACert),
		"nodeCert":       string(nodeCert.CertPEM),
		"nodeKey":        string(nodeCert.KeyPEM),
		"agentToken":     agentToken, // needed for cert renewal
		"serverIP":       serverIP.Addr().String(),
		"nebulaIP":       node.NebulaIP,
		"lighthousePort": lighthousePort,
		"dnsDomain":      network.DNSDomain,
	}
	if h.LighthouseHost != "" {
		resp["lighthouseHost"] = h.LighthouseHost
	}
	writeJSON(w, resp)
}

func generateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

