package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/trustos/hopssh/internal/auth"
	"github.com/trustos/hopssh/internal/db"
	"github.com/trustos/hopssh/internal/pki"
)

const nodeCertDuration = 24 * time.Hour // short-lived, auto-renewed by agent

// EnrollHandler manages node enrollment.
type EnrollHandler struct {
	Networks *db.NetworkStore
	Nodes    *db.NodeStore
	Endpoint string // public URL of this server (e.g. "https://hopssh.com")
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
	if err != nil || network == nil || network.UserID != user.ID {
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

	installCmd := fmt.Sprintf("curl -fsSL %s/install | sudo bash -s -- --token %s", h.Endpoint, enrollToken)

	writeJSONStatus(w, http.StatusCreated, map[string]interface{}{
		"nodeId":          node.ID,
		"enrollmentToken": enrollToken,
		"installCommand":  installCmd,
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

	// Parse the node's pre-allocated Nebula IP.
	nodeIP, err := pki.SubnetIP(network.NebulaSubnet, parseNodeIndex(node.NebulaIP)+2)
	if err != nil {
		// Fallback: use the stored IP directly.
		var parseErr error
		nodeIP, parseErr = parsePrefix(node.NebulaIP)
		if parseErr != nil {
			http.Error(w, "invalid node IP", http.StatusInternalServerError)
			return
		}
	}

	// Issue node certificate.
	nodeCert, err := pki.IssueCert(network.NebulaCACert, network.NebulaCAKey,
		fmt.Sprintf("node-%s", node.ID[:8]), nodeIP, []string{"agent"}, nodeCertDuration)
	if err != nil {
		http.Error(w, "failed to issue node cert: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Complete enrollment: store cert, consume token.
	if err := h.Nodes.CompleteEnrollment(node.ID, nodeCert.CertPEM, nodeCert.KeyPEM, body.Hostname, body.OS, body.Arch); err != nil {
		http.Error(w, "failed to complete enrollment: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Compute server's Nebula IP for static_host_map.
	serverIP, _ := pki.ServerAddress(network.NebulaSubnet)

	writeJSON(w, map[string]interface{}{
		"nodeId":     node.ID,
		"caCert":     string(network.NebulaCACert),
		"nodeCert":   string(nodeCert.CertPEM),
		"nodeKey":    string(nodeCert.KeyPEM),
		"agentToken": node.AgentToken,
		"serverIP":   serverIP.Addr().String(),
		"nebulaIP":   node.NebulaIP,
	})
}

func generateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// parseNodeIndex extracts the host index from a CIDR IP like "10.42.1.3/24" → 3.
func parseNodeIndex(cidr string) int {
	p, err := parsePrefix(cidr)
	if err != nil {
		return 0
	}
	octets := p.Addr().As4()
	return int(octets[3]) - 2 // .2 = node 0, .3 = node 1, etc.
}

func parsePrefix(s string) (pki.NetipPrefix, error) {
	return pki.ParsePrefix(s)
}
