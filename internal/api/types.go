package api

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var dnsUnsafe = regexp.MustCompile(`[^a-z0-9-]`)

// sanitizeDNSName converts a hostname to a DNS-safe short name.
// "Yavors-MacBook-Pro.local" → "yavors-macbook-pro"
// "my-server.example.com" → "my-server"
func sanitizeDNSName(hostname string) string {
	name := strings.ToLower(hostname)
	// Take only the first label (before first dot).
	if idx := strings.Index(name, "."); idx >= 0 {
		name = name[:idx]
	}
	// Replace unsafe chars with hyphens, collapse multiple hyphens.
	name = dnsUnsafe.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	// Collapse consecutive hyphens.
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	if name == "" {
		return "node"
	}
	return name
}

// nodeStaleThreshold must be ≥ 2× the agent heartbeat interval
// (cmd/agent/renew.go:normalInterval, 60s) to avoid flapping on a
// single missed beat. 3 min = 3× normal heartbeat with headroom.
const nodeStaleThreshold = 3 * 60 // 3 minutes in seconds

// NodeStaleThresholdSeconds is the exported form used by the background
// sweeper in internal/db to decide which online nodes to flip to
// offline. Kept as a named export so external callers don't duplicate
// the literal.
const NodeStaleThresholdSeconds = nodeStaleThreshold

// nodeDegradedGraceSeconds is how long a node must have been running
// before it can be flagged as "degraded" (heartbeat-only, no peers).
// Fresh agents need a grace window to discover peers via the lighthouse
// — marking them degraded at t=0 would flap every enrollment. 5 minutes
// covers a reasonable warm-up on any platform including cold-start
// cellular.
const nodeDegradedGraceSeconds = 5 * 60

// nodeDegradedPeersStaleSeconds is the maximum age of a `peers_reported_at`
// timestamp that we still consider authoritative. Older than this and
// we assume the agent hasn't successfully reported peer state recently
// (even if its heartbeat is still being received) — insufficient signal
// to mark degraded; stay "online".
const nodeDegradedPeersStaleSeconds = 2 * 60

// jsonDetails serialises a small payload for persisted network events.
// Returns nil if the payload is empty or serialization fails — callers
// pass the returned value straight through to NetworkEventStore.Record,
// which already handles nil (details column stays NULL).
func jsonDetails(v map[string]any) *string {
	if len(v) == 0 {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	s := string(b)
	return &s
}

// effectiveStatus returns the display status, marking "online" nodes as "offline"
// if they haven't sent a heartbeat within the staleness threshold.
func effectiveStatus(status string, lastSeenAt *int64) string {
	if status != "online" || lastSeenAt == nil {
		return status
	}
	now := time.Now().Unix()
	if now-*lastSeenAt > int64(nodeStaleThreshold) {
		return "offline"
	}
	return status
}

// isDegraded reports whether an online node should be shown as
// "degraded" — heartbeat is fresh but the node has no mesh
// connectivity. This signals a portmap/NAT-traversal failure, firewall
// change, or interface routing problem that the heartbeat channel
// (plain HTTPS to the control plane) doesn't exercise.
//
// Rules — ALL must hold:
//   - Status is "online" AND heartbeat fresh (effectiveStatus gates us).
//   - Node is not a lighthouse (they don't report peers about themselves).
//   - Node has been around longer than the grace window — fresh agents
//     have seconds of legitimate no-peer state while the lighthouse
//     propagates endpoints.
//   - Peer state was reported recently (agent is still alive enough to
//     push it) AND both peer counters are zero.
//   - The network has >1 other non-lighthouse nodes — a single-node
//     network has 0 peers legitimately; no one to connect to.
//
// peersInNetwork is the count of OTHER online non-lighthouse nodes on
// the same network (i.e. potential peer targets for THIS node). The
// caller computes it from the full node list since this function runs
// per-node without cross-node access.
func isDegraded(
	status string,
	nodeType string,
	createdAt int64,
	peersReportedAt *int64,
	peersDirect *int64,
	peersRelayed *int64,
	peersInNetwork int,
) bool {
	if status != "online" {
		return false
	}
	if nodeType == "lighthouse" {
		return false
	}
	if peersInNetwork < 1 {
		return false // we are alone; "0 peers" is not a failure
	}
	now := time.Now().Unix()
	if now-createdAt < int64(nodeDegradedGraceSeconds) {
		return false // still within warm-up grace
	}
	if peersReportedAt == nil {
		return false // never reported peer state — could be old agent
	}
	if now-*peersReportedAt > int64(nodeDegradedPeersStaleSeconds) {
		return false // peer report is stale; insufficient signal
	}
	var d, r int64
	if peersDirect != nil {
		d = *peersDirect
	}
	if peersRelayed != nil {
		r = *peersRelayed
	}
	return d == 0 && r == 0
}

// countPotentialPeers returns the number of OTHER non-lighthouse nodes
// (excluding the one at excludeID) whose effectiveStatus is "online".
// Used to pass `peersInNetwork` into isDegraded so a single-node
// network isn't labelled degraded when a node legitimately has no
// peers to reach.
func countPotentialPeers[T any](nodes []T, excludeID string, accessor func(T) (id, nodeType, status string, lastSeenAt *int64)) int {
	count := 0
	for _, n := range nodes {
		id, nodeType, status, lastSeenAt := accessor(n)
		if id == excludeID {
			continue
		}
		if nodeType == "lighthouse" {
			continue
		}
		if effectiveStatus(status, lastSeenAt) == "online" {
			count++
		}
	}
	return count
}

// parseCapabilities converts a JSON capabilities string to a slice.
func parseCapabilities(capsJSON string) []string {
	if capsJSON == "" || capsJSON == "null" {
		return []string{}
	}
	var caps []string
	if err := json.Unmarshal([]byte(capsJSON), &caps); err != nil {
		return []string{}
	}
	return caps
}

// captureAgentIP extracts the client IP from an HTTP request.
// Used to record the agent's real IP for mesh tunnel creation.
func captureAgentIP(r *http.Request) string {
	if TrustedProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			for i, c := range xff {
				if c == ',' {
					return strings.TrimSpace(xff[:i])
				}
			}
			return strings.TrimSpace(xff)
		}
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	return ip
}

// writeJSON encodes v as JSON to w, logging errors instead of silently dropping them.
// Must be called BEFORE WriteHeader, or pass the status via writeJSONStatus.
func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[api] json encode error: %v", err)
	}
}

// writeJSONStatus writes a JSON response with a specific HTTP status code.
// Sets Content-Type before WriteHeader to ensure the header is sent.
func writeJSONStatus(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[api] json encode error: %v", err)
	}
}

// --- Request types ---

// RegisterRequest is the request body for user registration.
type RegisterRequest struct {
	Email    string `json:"email" example:"user@example.com"`
	Name     string `json:"name" example:"John Doe"`
	Password string `json:"password" example:"secretpassword"`
}

// LoginRequest is the request body for user login.
type LoginRequest struct {
	Email    string `json:"email" example:"user@example.com"`
	Password string `json:"password" example:"secretpassword"`
}

// CreateNetworkRequest is the request body for creating a network.
type CreateNetworkRequest struct {
	Name string `json:"name" example:"production"`
}

// EnrollRequest is the request body for agent enrollment.
type EnrollRequest struct {
	Token    string `json:"token" example:"a1b2c3d4..."`
	Hostname string `json:"hostname" example:"web-server-1"`
	OS       string `json:"os" example:"linux"`
	Arch     string `json:"arch" example:"arm64"`
}

// ExecRequest is the request body for command execution.
type ExecRequest struct {
	Command string   `json:"command" example:"ls"`
	Args    []string `json:"args" example:"-la,/var/log"`
	Dir     string   `json:"dir,omitempty" example:"/home/ubuntu"`
	Env     []string `json:"env,omitempty" example:"FOO=bar"`
}

// StartPortForwardRequest is the request body for starting a port forward.
type StartPortForwardRequest struct {
	RemotePort int `json:"remotePort" example:"5432"`
	LocalPort  int `json:"localPort" example:"15432"`
}

// --- Response types ---

// AuthResponse is returned after successful registration or login.
type AuthResponse struct {
	ID    string `json:"id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Email string `json:"email" example:"user@example.com"`
	Name  string `json:"name" example:"John Doe"`
	Token string `json:"token" example:"a1b2c3d4e5f6..."`
}

// UserResponse is returned for the current user.
type UserResponse struct {
	ID    string `json:"id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Email string `json:"email" example:"user@example.com"`
	Name  string `json:"name" example:"John Doe"`
}

// StatusResponse indicates whether any users exist.
type StatusResponse struct {
	HasUsers bool `json:"hasUsers" example:"true"`
}

// NetworkResponse is returned when creating or getting a network.
type NetworkResponse struct {
	ID             string `json:"id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Name           string `json:"name" example:"production"`
	Slug           string `json:"slug" example:"production"`
	Subnet         string `json:"subnet" example:"10.42.1.0/24"`
	NodeCount      int    `json:"nodeCount" example:"3"`
	LighthousePort *int64 `json:"lighthousePort" example:"42001"`
	DNSDomain      string `json:"dnsDomain" example:"hop"`
	CreatedAt      int64  `json:"createdAt" example:"1712361600"`
}

// CreateNodeResponse is returned when creating a node enrollment token.
type CreateNodeResponse struct {
	NodeID          string `json:"nodeId" example:"550e8400-e29b-41d4-a716-446655440000"`
	EnrollmentToken string `json:"enrollmentToken" example:"a1b2c3d4..."`
	Endpoint        string `json:"endpoint" example:"https://hopssh.com:9473"`
	NebulaIP        string `json:"nebulaIP" example:"10.42.1.2/24"`
}

// EnrollResponse is returned to the agent after successful enrollment.
type EnrollResponse struct {
	CACert     string `json:"caCert" example:"-----BEGIN NEBULA CERTIFICATE-----..."`
	NodeCert   string `json:"nodeCert" example:"-----BEGIN NEBULA CERTIFICATE-----..."`
	NodeKey    string `json:"nodeKey" example:"-----BEGIN NEBULA X25519 PRIVATE KEY-----..."`
	AgentToken string `json:"agentToken" example:"deadbeef1234..."`
	ServerIP   string `json:"serverIP" example:"10.42.1.1"`
	NebulaIP   string `json:"nebulaIP" example:"10.42.1.2/24"`
}

// PeerDetail mirrors the agent's per-peer report shape, re-serialized
// from the `nodes.peer_state` JSON blob. Drives the dashboard's
// per-peer drill-down table and the topology diagram edges.
type PeerDetail struct {
	VpnAddr          string `json:"vpnAddr" example:"10.42.1.7"`
	Direct           bool   `json:"direct" example:"true"`
	LastHandshakeSec int64  `json:"lastHandshakeSec,omitempty" example:"1712361600"`
	RemoteAddr       string `json:"remoteAddr,omitempty" example:"192.168.23.18:4242"`
}

// NodeResponse represents a node in API responses.
type NodeResponse struct {
	ID              string   `json:"id" example:"550e8400-e29b-41d4-a716-446655440000"`
	NetworkID       string   `json:"networkId" example:"550e8400-e29b-41d4-a716-446655440000"`
	Hostname        string   `json:"hostname" example:"web-server-1"`
	OS              string   `json:"os" example:"linux"`
	Arch            string   `json:"arch" example:"arm64"`
	NebulaIP        string   `json:"nebulaIP" example:"10.42.1.2/24"`
	AgentRealIP     *string  `json:"agentRealIP" example:"203.0.113.10"`
	NodeType        string   `json:"nodeType" example:"node"`
	ExposedPorts    *string  `json:"exposedPorts,omitempty"`
	DNSName         *string  `json:"dnsName,omitempty"`
	Capabilities    []string `json:"capabilities" example:"terminal,health,forward"`
	Status          string   `json:"status" example:"online"`
	LastSeenAt      *int64   `json:"lastSeenAt" example:"1712361600"`
	CreatedAt       int64    `json:"createdAt" example:"1712361600"`
	PeersDirect     *int64   `json:"peersDirect,omitempty" example:"3"`
	PeersRelayed    *int64   `json:"peersRelayed,omitempty" example:"1"`
	PeersReportedAt *int64   `json:"peersReportedAt,omitempty" example:"1712361600"`
	AgentVersion    *string  `json:"agentVersion,omitempty" example:"v0.9.15"`
	// Connectivity is derived from PeersDirect / PeersRelayed at serialize time.
	// Values: "" (unknown — agent hasn't reported), "idle" (no peers),
	// "direct" (all peers direct), "relayed" (all peers relayed),
	// "mixed" (some direct, some relayed). Skipped entirely for lighthouse nodes.
	Connectivity string       `json:"connectivity,omitempty" example:"direct"`
	Peers        []PeerDetail `json:"peers,omitempty"` // parsed from peer_state JSON; nil if never reported
}

// parsePeerState decodes the agent-reported JSON blob into a structured
// slice. Returns nil on missing/empty/invalid — callers treat that as
// "no data reported yet" (the UI renders nothing).
func parsePeerState(raw *string) []PeerDetail {
	if raw == nil || *raw == "" || *raw == "null" {
		return nil
	}
	var out []PeerDetail
	if err := json.Unmarshal([]byte(*raw), &out); err != nil {
		return nil
	}
	return out
}

// deriveConnectivity translates the raw direct/relayed peer counts into
// a single display state. Returns "" for nodes that haven't reported,
// so the frontend can tell "unknown" from "idle".
func deriveConnectivity(peersDirect, peersRelayed *int64, nodeType string) string {
	if nodeType == "lighthouse" {
		return "" // meaningless for the relay itself
	}
	if peersDirect == nil && peersRelayed == nil {
		return ""
	}
	var d, r int64
	if peersDirect != nil {
		d = *peersDirect
	}
	if peersRelayed != nil {
		r = *peersRelayed
	}
	switch {
	case d == 0 && r == 0:
		return "idle"
	case r == 0:
		return "direct"
	case d == 0:
		return "relayed"
	default:
		return "mixed"
	}
}

// HealthResponse is returned from the agent health check.
type HealthResponse struct {
	Status   string `json:"status" example:"ok"`
	Hostname string `json:"hostname" example:"web-server-1"`
	OS       string `json:"os" example:"linux"`
	Arch     string `json:"arch" example:"arm64"`
	Uptime   string `json:"uptime" example:"2h30m15s"`
}

// PortForwardResponse represents an active port forward.
type PortForwardResponse struct {
	ID         string `json:"id" example:"fwd-1"`
	NetworkID  string `json:"networkId" example:"550e8400-..."`
	NodeID     string `json:"nodeId" example:"550e8400-..."`
	RemotePort int    `json:"remotePort" example:"5432"`
	LocalPort  int    `json:"localPort" example:"15432"`
	LocalAddr  string `json:"localAddr" example:"127.0.0.1:15432"`
	CreatedAt  int64  `json:"createdAt" example:"1712361600"`
}

// ErrorResponse is returned on errors.
type ErrorResponse struct {
	Error string `json:"error" example:"invalid credentials"`
}

// --- Device flow types ---

// DeviceCodeResponse is returned when requesting a device code.
type DeviceCodeResponse struct {
	DeviceCode      string `json:"deviceCode" example:"abc123..."`
	UserCode        string `json:"userCode" example:"HOP-K9M2"`
	VerificationURI string `json:"verificationURI" example:"/device"`
	ExpiresIn       int    `json:"expiresIn" example:"600"`
	Interval        int    `json:"interval" example:"5"`
}

// DevicePollRequest is the request body for polling device code status.
type DevicePollRequest struct {
	DeviceCode string `json:"deviceCode" example:"abc123..."`
}

// DeviceAuthorizeRequest is the request body for authorizing a device code.
type DeviceAuthorizeRequest struct {
	UserCode  string `json:"userCode" example:"HOP-K9M2"`
	NetworkID string `json:"networkId" example:"550e8400-..."`
}

// BundleResponse is returned when creating an enrollment bundle.
type BundleResponse struct {
	BundleURL string `json:"bundleUrl" example:"https://hopssh.com/api/bundles/abc123"`
	ExpiresIn int    `json:"expiresIn" example:"900"`
}

// --- Cert renewal types ---

// RenewRequest is the request body for certificate renewal.
type RenewRequest struct {
	NodeID string `json:"nodeId" example:"550e8400-e29b-41d4-a716-446655440000"`
}

// RenewResponse is returned after successful cert renewal.
type RenewResponse struct {
	NodeCert  string `json:"nodeCert" example:"-----BEGIN NEBULA CERTIFICATE-----..."`
	NodeKey   string `json:"nodeKey" example:"-----BEGIN NEBULA X25519 PRIVATE KEY-----..."`
	ExpiresIn int    `json:"expiresIn" example:"86400"`
}
