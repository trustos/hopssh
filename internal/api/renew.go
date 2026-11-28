package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/slackhq/nebula"

	"github.com/trustos/hopssh/internal/db"
	"github.com/trustos/hopssh/internal/fwpolicy"
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
	DNSRecords     *db.DNSRecordStore
	EventHub       *EventHub
	Events         *db.NetworkEventStore
	NetworkManager *mesh.NetworkManager
	// Firewall: the heartbeat answer of a managed network carries the
	// policy for the device that asked, and its report is stored (phase 2
	// of docs/wiki/concepts/firewall-rules.md). Nil: neither.
	Firewall *db.FirewallStore

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
	if err != nil {
		// Not 401, which tells the agent that its node was deleted.
		log.Printf("[renew] node %s: %v", body.NodeID, err)
		http.Error(w, "failed to load node", http.StatusInternalServerError)
		return
	}
	if node == nil {
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
	if err := h.Nodes.UpdateCert(node.ID, nodeCert.CertPEM, nodeCert.KeyPEM); errors.Is(err, db.ErrNodeGone) {
		// Deleted since the lookup above: the cert is not recorded, so no
		// blocklist would know it.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	} else if errors.Is(err, db.ErrTooManyCerts) {
		// A flood of renewals: their certs would not fit on the blocklist
		// once the node is deleted (db.MaxHeldCerts). The agent waits after
		// a 429 and renews again once older certs expire.
		log.Printf("[renew] node %s: refused, it holds %d certificates that have not expired", node.ID, db.MaxHeldCerts)
		w.Header().Set("Retry-After", "3600")
		http.Error(w, "too many certificates", http.StatusTooManyRequests)
		return
	} else if err != nil {
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
		// ClientType: "desktop" (bundled inside / installed by the macOS
		// .app) or "cli" (standalone CLI binary). Build-baked at the
		// agent (-X main.clientType=...). Empty / absent on legacy
		// agents — server treats that as unknown and renders "—".
		ClientType   *string           `json:"clientType,omitempty"`
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
		// The machine's own OS and arch, from the agent's runtime
		// (v0.11.31). Enrollment is the only other writer, and the
		// token path posted a literal "linux" until 72f2165 — which is
		// not in any release yet — so this is where an installed
		// machine's row learns what it really is. Optional; old agents
		// omit it.
		OS   string `json:"os,omitempty"`
		Arch string `json:"arch,omitempty"`
		// Firewall: what the agent runs of the policy it got from the
		// answer before (phase 2). Absent on agents that don't apply rules.
		Firewall *fwpolicy.Ack `json:"firewall,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.NodeID == "" {
		http.Error(w, "nodeId is required", http.StatusBadRequest)
		return
	}

	node, err := h.Nodes.Get(body.NodeID)
	if err != nil {
		// Not 401, which tells the agent that its node was deleted.
		log.Printf("[heartbeat] node %s: %v", body.NodeID, err)
		http.Error(w, "failed to load node", http.StatusInternalServerError)
		return
	}
	if node == nil {
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

	// Persist client_type out-of-band of the buffered heartbeat path.
	// It's build-baked at the agent + rarely changes, so a per-heartbeat
	// idempotent UPDATE-by-PK is the cheapest path that doesn't require
	// regenerating the sqlc heartbeat query.
	if body.ClientType != nil {
		_ = h.Nodes.SetClientType(node.ID, strings.TrimSpace(*body.ClientType))
	}

	// Same out-of-band path for the machine's OS and arch. The row keeps
	// whatever enrollment wrote and nothing else ever corrects it, and
	// enrollment guesses: the token body used to say "linux" for every
	// machine, and the device flow falls back to this server's own OS.
	// So this is where a Windows node stops being listed as "linux".
	// Checked against the row first: this handler runs every 30s per
	// node, and the beat itself is buffered precisely to avoid a write
	// each time.
	if (body.OS != "" && body.OS != node.OS) || (body.Arch != "" && body.Arch != node.Arch) {
		_ = h.Nodes.SetPlatform(node.ID, body.OS, body.Arch)
	}

	// A report that is not one is dropped, and the beat still answers: a
	// bug in the agent's report must not take the device offline.
	if h.Firewall != nil && body.Firewall != nil {
		if a, ok := cleanReport(*body.Firewall); ok {
			if err := h.Firewall.RecordReport(node.ID, a); err != nil {
				log.Printf("[heartbeat] node %s: firewall report: %v", node.ID, err)
			}
		}
	} else if h.Firewall != nil {
		if err := h.Firewall.NoReport(node.ID); err != nil {
			log.Printf("[heartbeat] node %s: firewall report: %v", node.ID, err)
		}
	}

	// Phase G: cache agent's self-reported endpoints. Layer 1 (v0.10.27):
	// each endpoint carries its own expiry derived from the agent's
	// NAT-PMP lease lifetime (when reported via SelfEndpointLifetimesSec
	// in matching order) OR the global fallback selfEndpointHintTTL.
	//
	// Empty SelfEndpoints CLEARS any prior entry (agent explicitly has
	// nothing to report, e.g. portmap dropped + no useful interface IPs).
	//
	// Only what a peer can send to is kept, at most maxSelfEndpoints
	// (selfEndpointHints); a report with nothing usable clears it too.
	if body.SelfEndpoints != nil {
		hints, dropped := selfEndpointHints(body.SelfEndpoints, body.SelfEndpointLifetimesSec, time.Now())
		if dropped > 0 {
			log.Printf("[heartbeat] node %s: %d of %d self-endpoints dropped (not an address a peer can send to, a duplicate, or over %d of a family)",
				node.ID, dropped, len(body.SelfEndpoints), maxSelfEndpoints)
		}
		if len(hints) > 0 {
			h.selfEndpointCache.Store(node.ID, &selfEndpointHint{endpoints: hints})
		} else {
			h.selfEndpointCache.Delete(node.ID)
		}
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
	peers, peersErr := h.Nodes.ListForNetwork(node.NetworkID)
	var peerIPs []string
	var relayIPs []string
	peerEndpoints := map[string][]string{}
	// peerInfo: name + DNS hostnames keyed by mesh IP, so the agent can
	// surface human-friendly identifiers in the desktop client's peers
	// list. Populated alongside peerEndpoints; backwards-compatible with
	// older agents (the field is optional in the response).
	type peerInfoEntry struct {
		Name           string   `json:"name,omitempty"`
		DnsHostname    string   `json:"dnsHostname,omitempty"`
		CustomDnsNames []string `json:"customDnsNames,omitempty"`
		// OS is the peer's operating system (runtime.GOOS — "darwin",
		// "linux", "windows", etc). Phase II.2 (v0.11.2): surfaced so
		// the desktop client can render a per-peer OS label. Backwards-
		// compatible: omitempty + older agents simply ignore it.
		OS string `json:"os,omitempty"`
		// NodeID is the peer's node UUID. Phase II.3 (v0.11.3):
		// surfaced so the desktop client can route to the dashboard's
		// per-node terminal page (/terminal/{networkId}/{nodeId}).
		NodeID string `json:"nodeId,omitempty"`
	}
	peerInfo := map[string]peerInfoEntry{}

	// User-defined DNS records for this network: build an IP→[]hostname
	// index once so the per-peer loop is O(1).
	customDnsByIP := map[string][]string{}
	if h.DNSRecords != nil {
		netRow, _ := h.Networks.Get(node.NetworkID)
		domainSuffix := ""
		if netRow != nil {
			domainSuffix = netRow.DNSDomain
		}
		records, _ := h.DNSRecords.ListForNetwork(node.NetworkID)
		for _, r := range records {
			ip := strings.TrimSuffix(r.NebulaIP, "/24")
			fqdn := r.Name
			if domainSuffix != "" {
				fqdn = r.Name + "." + domainSuffix
			}
			customDnsByIP[ip] = append(customDnsByIP[ip], fqdn)
		}
	}

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

		// Populate the human-readable identifiers for this peer.
		// Hostname always present; DnsName populated only after enrollment
		// completes (legacy nodes may have nil); custom DNS records are
		// optional and may be empty.
		entry := peerInfoEntry{Name: p.Hostname, OS: p.OS, NodeID: p.ID}
		if p.DNSName != nil {
			entry.DnsHostname = *p.DNSName
		}
		if customs := customDnsByIP[ip]; len(customs) > 0 {
			entry.CustomDnsNames = customs
		}
		peerInfo[ip] = entry
	}

	resp := map[string]interface{}{}
	// Phase II.3 (v0.11.3): include the network's UUID so the agent can
	// surface it via /local/status. The desktop client uses it to
	// construct the dashboard's terminal URL (/terminal/{networkId}/
	// {nodeId}). Agent persists the value once received; idempotent for
	// stable enrollments.
	resp["networkId"] = node.NetworkID
	if len(peerIPs) > 0 {
		resp["peers"] = peerIPs
	}
	if len(relayIPs) > 0 {
		resp["relays"] = relayIPs
	}
	if len(peerEndpoints) > 0 {
		resp["peerEndpoints"] = peerEndpoints
	}
	if len(peerInfo) > 0 {
		resp["peerInfo"] = peerInfo
	}
	if amRelay {
		resp["amRelay"] = true
	}

	// Roadmap #5 — Subnet routing. Push the requesting node's route
	// list down so the agent can write a `tun.unsafe_routes` block in
	// nebula.yaml on the next reload. Empty list omits the field
	// (backwards-compatible — older agents ignore it).
	if routes, _ := h.Nodes.GetRoutes(node.ID); len(routes) > 0 {
		out := make([]string, 0, len(routes))
		for _, r := range routes {
			out = append(out, r.Route)
		}
		resp["routes"] = out
	}

	// Phase 2: the policy for this device, when the network is managed. On
	// a failed read it is left out, and the agent keeps what it runs: a
	// policy built from a partial list of nodes would drop their rules.
	if h.Firewall != nil && peersErr == nil {
		preset, rules, managed, err := h.Firewall.Policy(node.NetworkID)
		if err != nil {
			log.Printf("[heartbeat] node %s: firewall policy: %v", node.ID, err)
		} else if managed {
			resp["firewall"] = fwpolicy.Resolve(preset, rules, node.ID, nodeAddrs(peers))
		}
	}

	// The certificates of the network's deleted nodes, which the device
	// gives Nebula's pki.blocklist (docs/wiki/concepts/deleted-devices.md).
	// Sent always, an empty list too, as it replaces what the agent holds;
	// left out on a failed read, and the agent keeps what it has.
	if list, err := h.Nodes.Blocklist(node.NetworkID, time.Now()); err != nil {
		log.Printf("[heartbeat] node %s: blocklist: %v", node.ID, err)
	} else {
		resp["blocklist"] = list
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

// maxSelfEndpoints is how many self-endpoints of a device the server keeps
// per address family: the most Nebula takes for a host (the rest it drops).
// A peer gets them after what the lighthouse knows of the device, so its
// Nebula may take fewer. Every peer gets them in each heartbeat answer and
// writes them to its peer cache, so without a bound one device could put a
// megabyte in all of them.
const maxSelfEndpoints = nebula.MaxRemotes

// selfEndpointHints is what the server keeps of the self-endpoints of a
// heartbeat: in the order sent, those a peer can send to, normalized, each
// once, at most maxSelfEndpoints per address family, with the lifetime sent
// at the same index.
// dropped is how many it left out. The agent sends only such addresses
// (internal/client/self_endpoints.go); anything else is a broken or hostile
// device, and junk in front would push the real addresses out of the ones
// Nebula takes.
func selfEndpointHints(endpoints []string, lifetimesSec []int, now time.Time) (hints []endpointHint, dropped int) {
	seen := map[netip.AddrPort]struct{}{}
	perFamily := map[bool]int{} // by Is4
	for i, s := range endpoints {
		ap, err := netip.ParseAddrPort(s)
		if err != nil || !peerCanSendTo(ap) {
			dropped++
			continue
		}
		ap = netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
		if _, dup := seen[ap]; dup || perFamily[ap.Addr().Is4()] == maxSelfEndpoints {
			dropped++
			continue
		}
		seen[ap] = struct{}{}
		perFamily[ap.Addr().Is4()]++
		expiresAt := now.Add(selfEndpointHintTTL)
		// If the agent reported a per-endpoint lifetime, use it.
		// Apply 50% safety margin per RFC 6886 §3.3 — peers must
		// stop dialing before the router actually drops the lease,
		// otherwise the brief window between actual expiry and
		// our pruning lets the router reassign the port and we
		// race to a different host.
		// Compared in seconds: a lifetime too large for a Duration (no
		// NAT-PMP one is) would wrap and expire the address at once.
		if i < len(lifetimesSec) && lifetimesSec[i] > 0 {
			if half := lifetimesSec[i] / 2; half < int(selfEndpointHintTTL/time.Second) {
				expiresAt = now.Add(time.Duration(half) * time.Second)
			}
		}
		hints = append(hints, endpointHint{addr: ap.String(), expiresAt: expiresAt})
	}
	return hints, dropped
}

// peerCanSendTo: ap is an address a peer elsewhere can send UDP to. Not
// port 0; not with a zone, which names an interface of the device itself
// and can be any length; not unspecified, loopback, multicast, link-local,
// 0.0.0.0/8 or 240.0.0.0/4 (255.255.255.255 among them); and not in the
// mesh (10.42.0.0/16, every network's): that one would go into the tunnel
// it is meant to set up. An IPv4 address written as IPv6 is judged as IPv4.
func peerCanSendTo(ap netip.AddrPort) bool {
	if ap.Port() == 0 || ap.Addr().Zone() != "" {
		return false
	}
	a := ap.Addr().Unmap()
	if !a.IsValid() || a.IsUnspecified() || a.IsLoopback() || a.IsMulticast() ||
		a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() {
		return false
	}
	return !thisNetwork.Contains(a) && !reserved.Contains(a) && !meshRange.Contains(a)
}

var (
	// meshRange holds the mesh addresses of every network (db.AllocateSubnet).
	meshRange = netip.MustParsePrefix("10.42.0.0/16")
	// Nobody can send to "this network" (RFC 1122) or to the reserved
	// range, which holds the broadcast address.
	thisNetwork = netip.MustParsePrefix("0.0.0.0/8")
	reserved    = netip.MustParsePrefix("240.0.0.0/4")
)

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
