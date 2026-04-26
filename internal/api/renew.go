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

// selfEndpointHintTTL is the FALLBACK expiry applied to endpoints whose
// agent didn't report a per-endpoint lifetime hint (old agent build, or
// LAN interface address with no router lease). Heartbeats fire every
// ~5 min in steady state; 15 min absorbs one missed beat plus jitter
// while still expiring stale data fast enough that peers don't dial
// dead addresses.
//
// Layer 1 (v0.10.27): per-endpoint lifetimes from RFC 6886 NAT-PMP leases
// take precedence when present. The fallback only applies when the agent
// didn't supply a hint.
const selfEndpointHintTTL = 15 * time.Minute

// selfEndpointHint is the cached value: per-endpoint expiry timestamps.
// Each endpoint has its own expiry derived from the NAT-PMP lease (when
// reported) or fallback (selfEndpointHintTTL after the heartbeat).
//
// Lifetime semantics:
//   - The HINT itself lives until the next heartbeat from this nodeID
//     overwrites it (sync.Map.Store).
//   - INDIVIDUAL endpoints inside expire on their own schedule per
//     `expiresAt`. mergePeerEndpoints filters out past-expiry entries
//     before serving them to other peers.
type selfEndpointHint struct {
	endpoints []endpointHint
}

// endpointHint pairs an endpoint string with its computed expiry
// timestamp. expiresAt is absolute wall-clock time (server's clock).
type endpointHint struct {
	addr      string
	expiresAt time.Time
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
	//
	// listenPort is ALSO intentionally NOT pushed (Fix A, v0.10.26). The
	// server has no per-node knowledge of which UDP port a given node's
	// agent allocated — there is no `listen_port` column on the `nodes`
	// table. Pushing the compile-time constant `nebulacfg.ListenPort`
	// (4242) silently corrupted multi-enrollment agents whose secondary
	// enrollment was allocated 4243+ via `NextAvailableListenPort`. The
	// agent's `enrollments.json` is the source of truth; `ensureP2PConfig`
	// already self-heals nebula.yaml from `Enrollment.ListenPort` at
	// boot and on every renewal.

	writeJSON(w, map[string]interface{}{
		"nodeCert":  string(nodeCert.CertPEM),
		"nodeKey":   string(nodeCert.KeyPEM),
		"expiresIn": int(renewCertDuration.Seconds()),
		"nebulaConfig": map[string]interface{}{
			"useRelays":  &useRelays,
			"punchBack":  &punchBack,
			"punchDelay": nebulacfg.PunchDelay,
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
		// SelfEndpointLifetimesSec (Layer 1, v0.10.27): per-endpoint
		// router-reported lease lifetime in seconds. MATCHING-ORDER
		// with SelfEndpoints. 0 (or absent) = no hint, server applies
		// default TTL. Used to bound how long a NAT-PMP-mapped endpoint
		// stays in the lighthouse cache after the source rotates —
		// the router may reassign the same external port to a different
		// internal host once the lease expires (CGNAT port reuse), so
		// we MUST stop dialing the old address once its lease is gone.
		// Optional; old agents omit it.
		SelfEndpointLifetimesSec []int `json:"selfEndpointLifetimesSec,omitempty"`
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

	// Phase G: cache agent's self-reported endpoints. Layer 1 (v0.10.27):
	// each endpoint carries its own expiry derived from the agent's
	// NAT-PMP lease lifetime (when reported via SelfEndpointLifetimesSec
	// in matching order) OR the global fallback selfEndpointHintTTL.
	//
	// Empty SelfEndpoints CLEARS any prior entry (agent explicitly has
	// nothing to report, e.g. portmap dropped + no useful interface IPs).
	if len(body.SelfEndpoints) > 0 {
		now := time.Now()
		hints := make([]endpointHint, 0, len(body.SelfEndpoints))
		for i, addr := range body.SelfEndpoints {
			expiresAt := now.Add(selfEndpointHintTTL)
			// If the agent reported a per-endpoint lifetime, use it.
			// Apply 50% safety margin per RFC 6886 §3.3 — peers must
			// stop dialing before the router actually drops the lease,
			// otherwise the brief window between actual expiry and
			// our pruning lets the router reassign the port and we
			// race to a different host.
			if i < len(body.SelfEndpointLifetimesSec) && body.SelfEndpointLifetimesSec[i] > 0 {
				safe := time.Duration(body.SelfEndpointLifetimesSec[i]/2) * time.Second
				if safe < selfEndpointHintTTL {
					expiresAt = now.Add(safe)
				}
			}
			hints = append(hints, endpointHint{addr: addr, expiresAt: expiresAt})
		}
		h.selfEndpointCache.Store(node.ID, &selfEndpointHint{endpoints: hints})
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
		// Resolve peer endpoints from BOTH the lighthouse's in-memory
		// cache (UDP-driven, may be stale on cellular) AND the peer's
		// self-reported endpoints from its own heartbeat (HTTPS-driven,
		// always reachable). The merge is the Phase G core fix —
		// guarantees endpoint distribution even when UDP-to-lighthouse
		// is blocked.
		var lighthouse []netip.AddrPort
		if netInst != nil {
			if vpn, err := netip.ParseAddr(ip); err == nil {
				lighthouse = netInst.PeerEndpoints(vpn)
			}
		}
		var hint *selfEndpointHint
		if v, ok := h.selfEndpointCache.Load(p.ID); ok {
			hint, _ = v.(*selfEndpointHint)
		}
		if merged := mergePeerEndpoints(lighthouse, hint, time.Now()); len(merged) > 0 {
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

// mergePeerEndpoints combines a peer's lighthouse-cached UDP endpoints
// (Nebula's in-memory advertise_addrs cache, populated via the UDP
// HostQuery flow) with the peer's self-reported endpoints from its
// HTTPS heartbeat (Phase G). Lighthouse entries come first in result
// order (preferred when both sources agree); hint entries fill gaps.
//
// Returns deduped, validated `IP:port` strings. Hint entries past
// their per-endpoint expiry are dropped — peers shouldn't dial dead
// addresses, and crucially shouldn't dial addresses whose router lease
// has expired (CGNAT may have reassigned the external port to a
// different host by then — Layer 1 fix).
//
// `now` is injected for testability (production passes time.Now()).
//
// This is the Phase G regression-test entry point: it's the exact
// merge logic the Heartbeat handler uses, factored out so unit tests
// can prove the cellular-idle silent-mesh failure mode is handled
// without spinning up a real DB + HTTP server.
func mergePeerEndpoints(lighthouse []netip.AddrPort, hint *selfEndpointHint, now time.Time) []string {
	out := make([]string, 0, len(lighthouse)+4)
	seen := map[string]struct{}{}
	for _, ap := range lighthouse {
		if !ap.IsValid() {
			continue
		}
		s := ap.String()
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if hint != nil {
		for _, e := range hint.endpoints {
			if !e.expiresAt.IsZero() && now.After(e.expiresAt) {
				// Endpoint past its NAT-PMP-derived (or fallback) expiry.
				// Drop — the router may have reassigned the port.
				continue
			}
			ap, err := netip.ParseAddrPort(e.addr)
			if err != nil || !ap.IsValid() {
				continue
			}
			norm := ap.String()
			if _, dup := seen[norm]; dup {
				continue
			}
			seen[norm] = struct{}{}
			out = append(out, norm)
		}
	}
	return out
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
