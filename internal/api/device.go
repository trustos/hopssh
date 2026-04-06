package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/trustos/hopssh/internal/auth"
	"github.com/trustos/hopssh/internal/mesh"
	"github.com/trustos/hopssh/internal/authz"
	"github.com/trustos/hopssh/internal/db"
	"github.com/trustos/hopssh/internal/pki"
)

const deviceNodeCertDuration = 24 * time.Hour // short-lived, auto-renewed by agent

// DeviceHandler manages the device authorization flow (RFC 8628) for enrollment.
type DeviceHandler struct {
	DeviceCodes    *db.DeviceCodeStore
	Networks       *db.NetworkStore
	Nodes          *db.NodeStore
	NetworkManager *mesh.NetworkManager
}

// RequestCode is called by the agent to initiate the device flow.
// Returns a device code (for polling) and a user code (for the human to enter).
// Public endpoint — no auth required.
// @Summary      Request device code
// @Description  Initiates the device authorization flow. Returns a device code for polling and a user code for the human to enter at /device.
// @Tags         enrollment
// @Produce      json
// @Success      200 {object} DeviceCodeResponse
// @Router       /api/device/code [post]
func (h *DeviceHandler) RequestCode(w http.ResponseWriter, r *http.Request) {
	dc, err := h.DeviceCodes.Create()
	if err != nil {
		http.Error(w, "failed to create device code: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]interface{}{
		"deviceCode":      dc.DeviceCode,
		"userCode":        dc.UserCode,
		"verificationURI": "/device",
		"expiresIn":       int(time.Until(time.Unix(dc.ExpiresAt, 0)).Seconds()),
		"interval":        5, // poll interval in seconds
	})
}

// Poll is called by the agent to check if the device code has been authorized.
// Public endpoint — no auth required.
// @Summary      Poll device code
// @Description  Agent polls this endpoint to check if the user has authorized the device code. Returns enrollment data when authorized.
// @Tags         enrollment
// @Accept       json
// @Produce      json
// @Param        body body DevicePollRequest true "Device code"
// @Success      200 {object} EnrollResponse "Enrollment complete"
// @Failure      400 {object} ErrorResponse "Missing device code"
// @Failure      403 {string} string "authorization_pending or expired"
// @Router       /api/device/poll [post]
func (h *DeviceHandler) Poll(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var body struct {
		DeviceCode string `json:"deviceCode"`
		Hostname   string `json:"hostname"`
		OS         string `json:"os"`
		Arch       string `json:"arch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.DeviceCode == "" {
		http.Error(w, "deviceCode is required", http.StatusBadRequest)
		return
	}

	// First check status without claiming (cheap read for pending/expired).
	dc, err := h.DeviceCodes.GetByDeviceCode(body.DeviceCode)
	if err != nil || dc == nil {
		http.Error(w, "invalid device code", http.StatusBadRequest)
		return
	}
	if time.Now().Unix() > dc.ExpiresAt {
		http.Error(w, "expired_token", http.StatusForbidden)
		return
	}
	if dc.Status == "pending" {
		http.Error(w, "authorization_pending", http.StatusForbidden)
		return
	}
	if dc.Status == "completed" {
		http.Error(w, "already completed", http.StatusBadRequest)
		return
	}

	// Atomically claim the authorized code — prevents duplicate enrollment.
	dc, err = h.DeviceCodes.ClaimAuthorized(body.DeviceCode)
	if err != nil {
		http.Error(w, "enrollment failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if dc == nil {
		// Another poll claimed it first, or status changed.
		http.Error(w, "authorization_pending", http.StatusForbidden)
		return
	}

	// Device code is claimed — perform enrollment.
	network, err := h.Networks.Get(*dc.NetworkID)
	if err != nil || network == nil {
		http.Error(w, "network not found", http.StatusInternalServerError)
		return
	}

	// Allocate node IP.
	nextIndex, err := h.Nodes.NextNodeIndex(*dc.NetworkID)
	if err != nil {
		http.Error(w, "failed to allocate node IP: "+err.Error(), http.StatusInternalServerError)
		return
	}
	nextIP, err := pki.NodeAddress(network.NebulaSubnet, nextIndex)
	if err != nil {
		http.Error(w, "failed to allocate node IP: "+err.Error(), http.StatusInternalServerError)
		return
	}

	agentToken := generateToken()
	nodeID := uuid.New().String()

	node := &db.Node{
		ID:         nodeID,
		NetworkID:  *dc.NetworkID,
		NebulaIP:   nextIP.String(),
		AgentToken: agentToken,
		Status:     "pending",
	}
	if err := h.Nodes.Create(node); err != nil {
		http.Error(w, "failed to create node: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Issue node certificate.
	nodeCert, err := pki.IssueCert(network.NebulaCACert, network.NebulaCAKey,
		fmt.Sprintf("node-%s", nodeID[:8]), nextIP, []string{"agent"}, deviceNodeCertDuration)
	if err != nil {
		http.Error(w, "failed to issue cert: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := h.Nodes.CompleteEnrollment(nodeID, nodeCert.CertPEM, nodeCert.KeyPEM, body.Hostname, body.OS, body.Arch); err != nil {
		http.Error(w, "failed to complete enrollment: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.DeviceCodes.SetNodeID(dc.DeviceCode, nodeID)
	h.Nodes.UpdateAgentRealIP(nodeID, captureAgentIP(r))
	if h.NetworkManager != nil {
		h.NetworkManager.RefreshDNS(*dc.NetworkID)
	}

	serverIP, _ := pki.ServerAddress(network.NebulaSubnet)
	lighthousePort := 0
	if network.LighthousePort != nil {
		lighthousePort = int(*network.LighthousePort)
	}

	writeJSON(w, map[string]interface{}{
		"nodeId":         nodeID,
		"caCert":         string(network.NebulaCACert),
		"nodeCert":       string(nodeCert.CertPEM),
		"nodeKey":        string(nodeCert.KeyPEM),
		"agentToken":     agentToken,
		"serverIP":       serverIP.Addr().String(),
		"nebulaIP":       node.NebulaIP,
		"lighthousePort": lighthousePort,
		"dnsDomain":      network.DNSDomain,
	})
}

// Authorize is called by the browser when the user enters the user code.
// Authenticated endpoint — requires session.
// @Summary      Authorize device code
// @Description  User enters the code shown on the server terminal. Selects a network and authorizes the device.
// @Tags         enrollment
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        body body DeviceAuthorizeRequest true "User code and network selection"
// @Success      200 {object} map[string]string
// @Failure      400 {object} ErrorResponse
// @Router       /api/device/authorize [post]
func (h *DeviceHandler) Authorize(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var body struct {
		UserCode  string `json:"userCode"`
		NetworkID string `json:"networkId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.UserCode == "" || body.NetworkID == "" {
		http.Error(w, "userCode and networkId are required", http.StatusBadRequest)
		return
	}

	// Verify user owns the network.
	network, err := h.Networks.Get(body.NetworkID)
	if err != nil || network == nil || !authz.CanAccessNetwork(user, network) {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}

	if err := h.DeviceCodes.Authorize(body.UserCode, user.ID, body.NetworkID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(w, map[string]string{"status": "authorized"})
}

// VerifyCode returns the status of a user code (for the browser to show details).
// Authenticated endpoint.
// @Summary      Verify device code
// @Description  Check if a user code is valid and pending authorization.
// @Tags         enrollment
// @Security     BearerAuth
// @Produce      json
// @Param        code path string true "User code (e.g. HOP-K9M2)"
// @Success      200 {object} map[string]interface{}
// @Failure      404 {object} ErrorResponse
// @Router       /api/device/verify/{code} [get]
func (h *DeviceHandler) VerifyCode(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	dc, err := h.DeviceCodes.GetByUserCode(code)
	if err != nil || dc == nil {
		http.Error(w, "invalid or expired code", http.StatusNotFound)
		return
	}

	writeJSON(w, map[string]interface{}{
		"userCode":  dc.UserCode,
		"status":    dc.Status,
		"expiresIn": int(time.Until(time.Unix(dc.ExpiresAt, 0)).Seconds()),
	})
}
