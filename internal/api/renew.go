package api

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/trustos/hopssh/internal/db"
	"github.com/trustos/hopssh/internal/nebulacfg"
	"github.com/trustos/hopssh/internal/pki"
)

const renewCertDuration = 24 * time.Hour

// RenewHandler manages agent certificate renewal and heartbeat.
type RenewHandler struct {
	Networks *db.NetworkStore
	Nodes    *db.NodeStore
	EventHub *EventHub
	Events   *db.NetworkEventStore
}

// Renew issues a fresh short-lived certificate for an enrolled node.
// Authenticated via the node's agent bearer token (not user session).
// @Summary      Renew node certificate
// @Description  Agent calls this to get a fresh short-lived Nebula certificate. Authenticated via the per-node bearer token.
// @Tags         enrollment
// @Accept       json
// @Produce      json
// @Param        body body RenewRequest true "Node ID"
// @Success      200 {object} RenewResponse
// @Failure      401 {object} ErrorResponse "Invalid token or node not found"
// @Router       /api/renew [post]
func (h *RenewHandler) Renew(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	// Extract bearer token.
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")

	var body struct {
		NodeID string `json:"nodeId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.NodeID == "" {
		http.Error(w, "nodeId is required", http.StatusBadRequest)
		return
	}

	// Load node and verify token.
	node, err := h.Nodes.Get(body.NodeID)
	if err != nil || node == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if subtle.ConstantTimeCompare([]byte(token), []byte(node.AgentToken)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Load network to get CA for signing.
	network, err := h.Networks.Get(node.NetworkID)
	if err != nil || network == nil {
		http.Error(w, "network not found", http.StatusInternalServerError)
		return
	}

	// Parse the node's existing Nebula IP.
	nodeIP, err := pki.ParsePrefix(node.NebulaIP)
	if err != nil {
		http.Error(w, "invalid node IP", http.StatusInternalServerError)
		return
	}

	// Issue fresh certificate — all nodes use "node" group.
	nodeCert, err := pki.IssueCert(network.NebulaCACert, network.NebulaCAKey,
		fmt.Sprintf("node-%s", node.ID[:8]), nodeIP, []string{"node"}, renewCertDuration)
	if err != nil {
		http.Error(w, "failed to issue cert: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Update agent's real IP (may change between renewals). Renewal
	// doesn't carry peer state — that flows through /api/heartbeat.
	h.Nodes.RecordHeartbeat(node.ID, captureAgentIP(r), nil, nil, nil)

	// Persist to DB.
	if err := h.Nodes.UpdateCert(node.ID, nodeCert.CertPEM, nodeCert.KeyPEM); err != nil {
		http.Error(w, "failed to update cert: "+err.Error(), http.StatusInternalServerError)
		return
	}

	mtu := nebulacfg.TunMTU
	useRelays := nebulacfg.UseRelays
	punchBack := nebulacfg.PunchBack
	listenPort := nebulacfg.ListenPort

	writeJSON(w, map[string]interface{}{
		"nodeCert":  string(nodeCert.CertPEM),
		"nodeKey":   string(nodeCert.KeyPEM),
		"expiresIn": int(renewCertDuration.Seconds()),
		"nebulaConfig": map[string]interface{}{
			"useRelays":  &useRelays,
			"punchBack":  &punchBack,
			"punchDelay": nebulacfg.PunchDelay,
			"mtu":        &mtu,
			"listenPort": &listenPort,
		},
	})
}

// Heartbeat updates a node's last_seen_at and status to "online".
// Called periodically by agents (all types) to report they're alive.
// POST /api/heartbeat — agent-authenticated via bearer token.
func (h *RenewHandler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")

	var body struct {
		NodeID       string            `json:"nodeId"`
		PeersDirect  *int64            `json:"peersDirect,omitempty"`
		PeersRelayed *int64            `json:"peersRelayed,omitempty"`
		Peers        []json.RawMessage `json:"peers,omitempty"` // re-serialized verbatim into peer_state
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.NodeID == "" {
		http.Error(w, "nodeId is required", http.StatusBadRequest)
		return
	}

	node, err := h.Nodes.Get(body.NodeID)
	if err != nil || node == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(node.AgentToken)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Re-serialize the peers array verbatim so the DB stores the JSON
	// the agent sent. nil when the agent omitted the field so COALESCE
	// preserves prior value server-side.
	var peerStatePtr *string
	if body.Peers != nil {
		if b, err := json.Marshal(body.Peers); err == nil {
			s := string(b)
			peerStatePtr = &s
		}
	}
	h.Nodes.RecordHeartbeat(node.ID, captureAgentIP(r), body.PeersDirect, body.PeersRelayed, peerStatePtr)

	if h.EventHub != nil {
		evt := map[string]any{"nodeId": node.ID, "status": "online"}
		if body.PeersDirect != nil {
			evt["peersDirect"] = *body.PeersDirect
		}
		if body.PeersRelayed != nil {
			evt["peersRelayed"] = *body.PeersRelayed
		}
		h.EventHub.Publish(node.NetworkID, Event{Type: "node.status", Data: evt})
	}
	// Persist only on actual transitions (e.g., offline→online). A
	// steady stream of "already online" heartbeats does NOT produce
	// log rows — the in-memory StatusTransition tracker coalesces.
	if h.Events != nil && h.Nodes.StatusTransition(node.ID, "online") {
		targetID := node.ID
		status := "online"
		h.Events.Record(node.NetworkID, "node.status", &targetID, &status, nil)
	}

	// Return online peer mesh IPs so the agent can pre-warm tunnels.
	peers, _ := h.Nodes.ListForNetwork(node.NetworkID)
	var peerIPs []string
	now := time.Now().Unix()
	for _, p := range peers {
		if p.ID == node.ID || p.NebulaIP == "" {
			continue
		}
		if p.LastSeenAt != nil && now-*p.LastSeenAt < 600 {
			ip := strings.TrimSuffix(p.NebulaIP, "/24")
			peerIPs = append(peerIPs, ip)
		}
	}

	if len(peerIPs) > 0 {
		writeJSON(w, map[string]interface{}{"peers": peerIPs})
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}
