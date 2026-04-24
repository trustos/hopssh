package api

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/trustos/hopssh/internal/db"
	"github.com/trustos/hopssh/internal/mesh"
	"github.com/trustos/hopssh/internal/nebulacfg"
	"github.com/trustos/hopssh/internal/pki"
)

const renewCertDuration = 24 * time.Hour

// selfEndpointHintTTL caps how long we trust an agent's self-reported
// endpoints after its last heartbeat. Heartbeats fire every ~5 min in
// steady state; 15 min absorbs one missed beat plus jitter while still
// expiring stale data fast enough that peers don't dial dead addresses.
const selfEndpointHintTTL = 15 * time.Minute

// selfEndpointHint is the cached value: endpoints + when the agent
// reported them. Lifetime tied to selfEndpointHintTTL.
type selfEndpointHint struct {
	endpoints []string
	updatedAt time.Time
}

// RenewHandler manages agent certificate renewal and heartbeat.
type RenewHandler struct {
	Networks       *db.NetworkStore
	Nodes          *db.NodeStore
	EventHub       *EventHub
	Events         *db.NetworkEventStore
	NetworkManager *mesh.NetworkManager

	// selfEndpointCache holds each agent's most-recent self-reported
	// reachable endpoints (populated from heartbeat body.SelfEndpoints).
	// Phase G: HTTPS-distributed peer endpoints, independent of UDP-to-
	// lighthouse advertise_addrs. Keyed by nodeID; entries auto-expire
	// via selfEndpointHintTTL. In-memory only — agents heartbeat
	// frequently enough that a server restart rebuilds the cache in
	// minutes.
	selfEndpointCache sync.Map // nodeID (string) -> *selfEndpointHint
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
	// doesn't carry peer state or agent version — those flow through
	// /api/heartbeat.
	h.Nodes.RecordHeartbeat(node.ID, captureAgentIP(r), nil, nil, nil, nil)

	// Persist to DB.
	if err := h.Nodes.UpdateCert(node.ID, nodeCert.CertPEM, nodeCert.KeyPEM); err != nil {
		http.Error(w, "failed to update cert: "+err.Error(), http.StatusInternalServerError)
		return
	}

	useRelays := nebulacfg.UseRelays
	punchBack := nebulacfg.PunchBack
	listenPort := nebulacfg.ListenPort

	// MTU is intentionally NOT pushed back. Two writers of nebula.yaml::tun.mtu
	// is one too many — the agent's `ensureP2PConfig` writes it from its OWN
	// compiled-in `nebulacfg.TunMTU` at startup, which is the source of truth
	// for that agent's binary. If we also push the server's compiled-in default
	// here, an OLDER server can clobber a NEWER agent's correct local value
	// during the renewal window. (Empirically observed 2026-04-23: a v0.10.15
	// control plane pushed mtu=1420 to a freshly-deployed v0.10.16 agent
	// (TunMTU=1380) right after the agent's local rewrite to 1380, leaving
	// the agent's nebula.yaml stuck at 1420 until manual intervention.)
	//
	// If a per-network admin-override-MTU feature is added later, this is
	// where it would re-appear — sent only when the network has an explicit
	// override, never as the global default.

	writeJSON(w, map[string]interface{}{
		"nodeCert":  string(nodeCert.CertPEM),
		"nodeKey":   string(nodeCert.KeyPEM),
		"expiresIn": int(renewCertDuration.Seconds()),
		"nebulaConfig": map[string]interface{}{
			"useRelays":  &useRelays,
			"punchBack":  &punchBack,
			"punchDelay": nebulacfg.PunchDelay,
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
		AgentVersion *string           `json:"agentVersion,omitempty"`
		// SelfEndpoints (Phase G): agent's own observed reachable
		// endpoints (NAT-PMP public + local interface IPs paired
		// with listen port). Cached server-side and merged into
		// peerEndpoints responses to OTHER agents — closes the
		// loop when this agent's UDP-to-lighthouse path is filtered
		// (e.g. iPhone Personal Hotspot). Optional; absent on older
		// agent builds.
		SelfEndpoints []string `json:"selfEndpoints,omitempty"`
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
	h.Nodes.RecordHeartbeat(node.ID, captureAgentIP(r), body.PeersDirect, body.PeersRelayed, peerStatePtr, body.AgentVersion)

	// Phase G: cache agent's self-reported endpoints. Validated and
	// deduped on read; here we just store the raw slice. Empty input
	// CLEARS any prior entry (agent explicitly has nothing to report,
	// e.g. portmap dropped + no useful interface IPs).
	if len(body.SelfEndpoints) > 0 {
		h.selfEndpointCache.Store(node.ID, &selfEndpointHint{
			endpoints: body.SelfEndpoints,
			updatedAt: time.Now(),
		})
	} else if body.SelfEndpoints != nil {
		h.selfEndpointCache.Delete(node.ID)
	}

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
	// Also surface peer-relay-capable nodes so agents can extend their
	// `relay.relays` list (Pillar 3) and signal whether THIS node has
	// the "relay" capability so it can set `am_relay: true`.
	//
	// `peerEndpoints` carries each peer's currently-advertised UDP
	// endpoints (reported advertise_addrs seen by the server's
	// lighthouse — includes NAT-PMP public mappings via patch 11). The
	// agent uses these to pre-populate its Nebula hostmap via patch 20's
	// AddStaticHostMap, so direct tunnel establishment works even when
	// the agent can't reach the UDP lighthouse (e.g. carrier-filtered
	// cellular to Oracle Cloud). Mirrors Tailscale's control-plane-
	// distributes-peer-endpoints model.
	peers, _ := h.Nodes.ListForNetwork(node.NetworkID)
	var peerIPs []string
	var relayIPs []string
	peerEndpoints := map[string][]string{}
	amRelay := false
	now := time.Now().Unix()

	var netInst *mesh.NetworkInstance
	if h.NetworkManager != nil {
		netInst, _ = h.NetworkManager.GetInstance(node.NetworkID)
	}

	for _, p := range peers {
		caps := parseCapabilitiesForRenew(p.Capabilities)
		if p.ID == node.ID {
			amRelay = caps["relay"]
			continue
		}
		if p.NebulaIP == "" {
			continue
		}
		if p.LastSeenAt == nil || now-*p.LastSeenAt >= 600 {
			continue
		}
		ip := strings.TrimSuffix(p.NebulaIP, "/24")
		peerIPs = append(peerIPs, ip)
		if caps["relay"] {
			relayIPs = append(relayIPs, ip)
		}
		// Look up the peer's currently-advertised UDP endpoints from the
		// lighthouse's in-memory cache. nil/empty is fine; the agent will
		// fall back to the normal UDP HostQuery flow if this info is
		// absent.
		merged := make([]string, 0, 4)
		seen := map[string]struct{}{}
		if netInst != nil {
			if vpn, err := netip.ParseAddr(ip); err == nil {
				if eps := netInst.PeerEndpoints(vpn); len(eps) > 0 {
					for _, ep := range eps {
						s := ep.String()
						if _, dup := seen[s]; dup {
							continue
						}
						seen[s] = struct{}{}
						merged = append(merged, s)
					}
				}
			}
		}
		// Phase G: also merge endpoints the peer has self-reported via
		// its HTTPS heartbeat. Independent of lighthouse UDP propagation
		// — covers agents whose UDP-to-lighthouse path is filtered.
		if v, ok := h.selfEndpointCache.Load(p.ID); ok {
			if hint, ok := v.(*selfEndpointHint); ok && time.Since(hint.updatedAt) < selfEndpointHintTTL {
				for _, s := range hint.endpoints {
					ap, err := netip.ParseAddrPort(s)
					if err != nil || !ap.IsValid() {
						continue
					}
					norm := ap.String()
					if _, dup := seen[norm]; dup {
						continue
					}
					seen[norm] = struct{}{}
					merged = append(merged, norm)
				}
			}
		}
		if len(merged) > 0 {
			peerEndpoints[ip] = merged
		}
	}

	resp := map[string]interface{}{}
	if len(peerIPs) > 0 {
		resp["peers"] = peerIPs
	}
	if len(relayIPs) > 0 {
		resp["relays"] = relayIPs
	}
	if len(peerEndpoints) > 0 {
		resp["peerEndpoints"] = peerEndpoints
	}
	if amRelay {
		resp["amRelay"] = true
	}
	if len(resp) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, resp)
}

// parseCapabilitiesForRenew unmarshals the JSON-array capability blob
// stored on Node into a set, defaulting to empty on parse failure.
// Local helper so renew.go doesn't need to import the parseCapabilities
// in types.go (which returns []string for serialization).
func parseCapabilitiesForRenew(capsJSON string) map[string]bool {
	out := map[string]bool{}
	if capsJSON == "" {
		return out
	}
	var arr []string
	if err := json.Unmarshal([]byte(capsJSON), &arr); err != nil {
		return out
	}
	for _, c := range arr {
		out[c] = true
	}
	return out
}
