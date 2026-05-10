package client

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/slackhq/nebula/cert"
	"github.com/trustos/hopssh/internal/buildinfo"
)

// localAPIBindAddr is the loopback bind address for the local API.
// Port 0 = kernel-assigned. Hardcoded to 127.0.0.1 to ensure no LAN exposure.
const localAPIBindAddr = "127.0.0.1:0"

// localAPITokenFile is the file (mode 0600) where the bearer token is
// persisted. The Tauri shell can read it when connecting to a system-
// installed daemon (where stdout isn't easily available).
const localAPITokenFile = "local-api-token"

// localAPIReadyPrefix is the magic prefix written to stdout once the
// server is ready. Tauri scrapes for this line.
const localAPIReadyPrefix = "HOPSSH_LOCAL_API:"

// localAPIServer carries dependencies the handlers need.
//
// Phase NN: previously the agent's main.go passed in *enrollmentRegistry,
// *instanceRegistry, connectFn, disconnectFn separately. Post-extraction
// the local API lives inside internal/client and reaches the same state
// through a *Client reference (same package, can read unexported
// registry fields directly).
type localAPIServer struct {
	client *Client
	token  string

	// events is a fan-out hub for SSE subscribers.
	events *localEventHub
}

// configDir returns the on-disk config root.
func (s *localAPIServer) configDir() string { return s.client.cfg.ConfigDir }

// enrolls returns the enrollment registry.
func (s *localAPIServer) enrolls() *enrollmentRegistry { return s.client.enrolls }

// instances returns the live mesh instance registry.
func (s *localAPIServer) instances() *instanceRegistry { return s.client.instances }

// localEvent + localEventHub + globalEventBus + publishToEventBus +
// newLocalEventHub + (*localEventHub).{subscribe,unsubscribe,publish}
// were extracted to events.go in Phase NN M1 so the watchdog code in
// keepalive.go could publish events without depending on local_api.go.
// Both files used to live in cmd/agent (one package); now they share
// internal/client and use the events.go canonical declarations.

// StartLocalAPI wires up the loopback HTTP API on 127.0.0.1:0, generates
// a fresh bearer token, persists it, prints HOPSSH_LOCAL_API:<addr>:<token>
// to stdout, and serves until ctx is cancelled. Non-fatal — returns the
// listener so callers can probe readiness, and an error on bind failure.
//
// Phase NN: pre-extraction this took (cfgDir, enrolls, instances,
// connectFn, disconnectFn) separately. Now takes a *Client which holds all
// of those. Connect/disconnect handlers call c.connect / c.disconnect
// (unexported, package-internal — same as before, with the closure
// indirection removed).
func StartLocalAPI(ctx context.Context, c *Client) error {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		// crypto/rand failure means the system entropy source is broken.
		// Per coding principles: panic — nothing is safe.
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	token := hex.EncodeToString(tokenBytes)

	srv := &localAPIServer{
		client: c,
		token:  token,
		events: newLocalEventHub(),
	}
	cfgDir := c.cfg.ConfigDir
	// Publish the hub so other goroutines (watchdog, etc.) can emit
	// events without holding a reference to srv.
	globalEventBus.Store(srv.events)

	tokenPath := filepath.Join(cfgDir, localAPITokenFile)
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		return fmt.Errorf("local-api: mkdir cfgDir: %w", err)
	}
	if err := atomicWrite(tokenPath, []byte(token+"\n"), 0600); err != nil {
		return fmt.Errorf("local-api: write token: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /local/health", srv.handleHealth)
	mux.HandleFunc("GET /local/status", srv.handleStatus)
	mux.HandleFunc("GET /local/peers", srv.handlePeers)
	mux.HandleFunc("GET /local/events", srv.handleEvents)
	mux.HandleFunc("POST /local/enroll/device-flow/start", srv.handleEnrollDeviceFlowStart)
	mux.HandleFunc("POST /local/enroll/device-flow/poll", srv.handleEnrollDeviceFlowPoll)
	mux.HandleFunc("POST /local/enroll/token", srv.handleEnrollToken)
	mux.HandleFunc("POST /local/connect", srv.handleConnect)
	mux.HandleFunc("POST /local/disconnect", srv.handleDisconnect)
	mux.HandleFunc("POST /local/leave", srv.handleLeave)
	mux.HandleFunc("POST /local/clipboard-sync", srv.handleClipboardSyncToggle)
	mux.HandleFunc("POST /local/renew", srv.handleForceRenew)

	authed := localAuthMiddleware(token, mux)

	ln, err := net.Listen("tcp", localAPIBindAddr)
	if err != nil {
		return fmt.Errorf("local-api: listen: %w", err)
	}

	httpSrv := &http.Server{
		Handler:           authed,
		ReadHeaderTimeout: 10 * time.Second,
	}

	addr := ln.Addr().String()
	fmt.Fprintf(os.Stdout, "%s%s:%s\n", localAPIReadyPrefix, addr, token)
	log.Printf("[local-api] listening on %s (token: %s..., file: %s)", addr, token[:8], tokenPath)

	// When running as the system-mode agent (launchd-spawned with
	// --mirror-dir set), write the token + port into the console
	// user's well-known mirror dir so the desktop .app can attach
	// without needing root to read /etc/hop-agent/local-api-token.
	// Best-effort; bundled mode is the recoverable fallback if this
	// fails.
	writeSystemMirrorFiles(systemMirrorDirOverride, token, addr)

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[local-api] serve: %v", err)
		}
	}()

	// Periodic status broadcast on the SSE bus so subscribers get a
	// heartbeat even when nothing has changed. Cheap; the JSON is tiny.
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				srv.events.publish(localEvent{
					Time: time.Now(),
					Type: "status",
					Data: srv.statusSnapshot(),
				})
			}
		}
	}()

	return nil
}

// localAuthMiddleware: bearer-token auth + 127.0.0.1-only enforcement.
func localAuthMiddleware(token string, next http.Handler) http.Handler {
	expected := "Bearer " + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "bad remote addr", http.StatusForbidden)
			return
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			http.Error(w, "loopback only", http.StatusForbidden)
			return
		}

		// Liberal CORS for the Tauri WebView. The loopback + bearer gates
		// already constrain who can reach this endpoint; CORS headers are
		// just so the browser-side fetch from the WebView doesn't get
		// blocked by same-origin policy.
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")

		// CORS preflight MUST be answered without requiring auth. The
		// browser sends OPTIONS automatically before any cross-origin
		// fetch that has a non-simple header (e.g. Authorization), and
		// per spec preflight does NOT carry credentials. If we reject
		// OPTIONS with 401, the browser blocks the actual GET — even
		// though the GET would succeed if it ran. Loopback gate above
		// is enough security here; an attacker who can hit the loopback
		// port can do anything from a real browser tab anyway.
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		auth := r.Header.Get("Authorization")
		if subtle.ConstantTimeCompare([]byte(auth), []byte(expected)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- Handlers ---

func (s *localAPIServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"version": buildinfo.Version,
		"commit":  buildinfo.Commit,
	})
}

// EnrollmentStatus is one entry in the status response. JSON-stable.
type EnrollmentStatus struct {
	Name           string `json:"name"`
	Endpoint       string `json:"endpoint"`
	NodeID         string `json:"nodeId"`
	// NetworkID is the server-side UUID for this enrollment's network.
	// Phase II.3 (v0.11.3): surfaced so the desktop client can
	// construct the dashboard's terminal URL (/terminal/{networkId}/
	// {nodeId}). Populated lazily from the heartbeat response — empty
	// until the first heartbeat completes after enrollment.
	NetworkID      string `json:"networkId,omitempty"`
	DNSDomain      string `json:"dnsDomain,omitempty"`
	TunMode        string `json:"tunMode,omitempty"`
	ListenPort     int    `json:"listenPort,omitempty"`
	MeshIP         string `json:"meshIp,omitempty"`
	NebulaIP       string `json:"nebulaIp,omitempty"`
	CertExpiresIn  string `json:"certExpiresIn,omitempty"`
	CertNotAfter   string `json:"certNotAfter,omitempty"`
	Connected      bool   `json:"connected"`
	PeersDirect    int    `json:"peersDirect"`
	PeersRelayed   int    `json:"peersRelayed"`
	LastError      string `json:"lastError,omitempty"`
	// ClipboardSync mirrors enrollment.ClipboardSync — Phase L
	// per-(device, network) opt-in flag for clipboard sync.
	ClipboardSync bool `json:"clipboardSync,omitempty"`
}

// LocalStatus is the GET /local/status response.
type LocalStatus struct {
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	ConfigDir     string `json:"configDir"`
	ServiceStatus string `json:"serviceStatus,omitempty"`
	// Hostname is the device's hostname (os.Hostname()). Phase EE F2:
	// surfaced so the desktop UI can render an account-identity-style
	// affordance ("device: Yavors-MacBook-Pro · endpoint: hopssh.com")
	// — for a self-hosted product the relevant identity is "which
	// device am I, against which control plane", not a user email.
	// Best-effort: empty string on os.Hostname() failure.
	Hostname string `json:"hostname,omitempty"`
	// RunMode is "bundled" when the agent is a child of a desktop .app
	// (or otherwise running out of a user configDir) and "system" when
	// it's the launchd-spawned daemon out of /etc/hop-agent. The
	// desktop UI uses this to drive the "Run in the background" toggle
	// state, replacing the previous developer-jargon "Service" row.
	RunMode         string             `json:"runMode"`
	Enrollments     []EnrollmentStatus `json:"enrollments"`
	ParallelInstall *ParallelInstall   `json:"parallelInstall,omitempty"`
}

// ParallelInstall describes signals of a parallel hop-agent install on
// the same host. Surfaced to the desktop UI so the onboarding flow can
// warn the user that a leftover install will block enrollment with a
// port-bind conflict, and offer a one-click Reset before they hit the
// device-flow path.
//
// Detection is best-effort and read-only — never mutates host state.
type ParallelInstall struct {
	// LaunchDaemon is true when /Library/LaunchDaemons/com.hopssh.agent.plist
	// exists on macOS. A leftover system install creates this; the
	// bundled .app does not. macOS-only signal.
	LaunchDaemon bool `json:"launchDaemon"`
	// LegacyConfigDir is true when /etc/hop-agent exists with our
	// expected layout (enrollments.json present). Indicates a previous
	// `sudo hop-agent install` left state behind.
	LegacyConfigDir bool `json:"legacyConfigDir"`
}

func (s *localAPIServer) statusSnapshot() map[string]any {
	st := s.buildStatus()
	// Marshal-roundtrip to a generic map for the SSE payload.
	b, _ := json.Marshal(st)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func (s *localAPIServer) buildStatus() LocalStatus {
	hostname, _ := os.Hostname() // Phase EE F2: best-effort; "" on failure.
	out := LocalStatus{
		Version:       buildinfo.Version,
		Commit:        buildinfo.Commit,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		ConfigDir:     s.configDir(),
		ServiceStatus: readServiceStatus(),
		Hostname:      hostname,
		RunMode:       deriveRunMode(s.configDir()),
		Enrollments:   []EnrollmentStatus{},
	}
	for _, e := range s.enrolls().List() {
		out.Enrollments = append(out.Enrollments, s.enrollmentStatus(e))
	}
	if pi := detectParallelInstall(s.configDir()); pi != nil {
		out.ParallelInstall = pi
	}
	return out
}

// detectParallelInstall returns non-nil when a leftover system-mode
// hop-agent install is present alongside the current (typically
// bundled) agent. Used by the desktop UI to warn the user before they
// trigger an enrollment that would fail with a port-bind conflict.
//
// `currentConfigDir` is the agent's own configDir. When the current
// agent IS the system-mode install (configDir == /etc/hop-agent), we
// suppress BOTH signals: the LaunchDaemon plist points at OURSELVES,
// and the legacy configDir IS our own. Reporting them as a parallel
// install would surface the warning banner on the post-A1 happy
// path, which is exactly the inverse of what we want.
func detectParallelInstall(currentConfigDir string) *ParallelInstall {
	if runtime.GOOS != "darwin" {
		return nil
	}
	// Self-suppression: when WE are the system agent, neither signal
	// is a "parallel" install — they're our own install.
	if currentConfigDir == "/etc/hop-agent" {
		return nil
	}
	pi := &ParallelInstall{}
	if _, err := os.Stat("/Library/LaunchDaemons/com.hopssh.agent.plist"); err == nil {
		pi.LaunchDaemon = true
	}
	if _, err := os.Stat("/etc/hop-agent/enrollments.json"); err == nil {
		pi.LegacyConfigDir = true
	}
	if !pi.LaunchDaemon && !pi.LegacyConfigDir {
		return nil
	}
	return pi
}

func (s *localAPIServer) enrollmentStatus(e *Enrollment) EnrollmentStatus {
	es := EnrollmentStatus{
		Name:          e.Name,
		Endpoint:      e.Endpoint,
		NodeID:        e.NodeID,
		NetworkID:     e.NetworkID,
		DNSDomain:     e.DNSDomain,
		TunMode:       e.TunMode,
		ListenPort:    e.ListenPort,
		ClipboardSync: e.ClipboardSync,
	}

	// Cert info (best-effort).
	certPath := filepath.Join(enrollmentDir(s.configDir(), e.Name), "node.crt")
	if certPEM, err := os.ReadFile(certPath); err == nil {
		if c, _, err := cert.UnmarshalCertificateFromPEM(certPEM); err == nil {
			if nets := c.Networks(); len(nets) > 0 {
				es.NebulaIP = nets[0].String()
				ip := nets[0].Addr().String()
				es.MeshIP = ip
			}
			es.CertNotAfter = c.NotAfter().UTC().Format(time.RFC3339)
			remaining := time.Until(c.NotAfter())
			if remaining > 0 {
				es.CertExpiresIn = remaining.Truncate(time.Minute).String()
			} else {
				es.LastError = "certificate expired"
			}
		}
	}

	// Live instance state.
	if inst := s.instances().get(e.Name); inst != nil {
		// Runtime TUN mode may differ from enroll-time. The agent
		// auto-upgrades userspace→kernel when started as root; that
		// upgrade writes the tun-mode file but NOT enrollments.json,
		// so reading e.TunMode lies. Override with runtime truth.
		es.TunMode = currentTunMode(inst)
		ctrl := inst.control()
		if ctrl != nil {
			direct, relayed, _, ok := collectPeerState(ctrl, inst.pathQuality)
			if ok {
				es.PeersDirect = direct
				es.PeersRelayed = relayed
			}
			// Phase P (UI honesty): Connected requires functional
			// connectivity, not just a Nebula struct in memory. Pre-Phase-P
			// the flag was set unconditionally on `ctrl != nil`, so a node
			// with an expired cert and 0 peers painted a green "connected"
			// pill — directly contradicting the inline LastError. Three
			// gates now apply, ALL must hold:
			//
			//   1. ctrl != nil (existing — Nebula instance allocated)
			//   2. cert not expired (LastError not set above)
			//   3. peers > 0 OR recent heartbeat success
			//
			// Gate 3 covers the first-startup window where the agent has
			// just come up, peers haven't done their first handshake yet,
			// but heartbeats ARE talking to the control plane successfully.
			// 3 minutes is generous — heartbeats fire every 60s in steady
			// state; a connected agent will refresh well within the window.
			const heartbeatGrace = 3 * time.Minute
			certValid := es.LastError == ""
			activeFlow := es.PeersDirect+es.PeersRelayed > 0
			recentHeartbeat := inst.heartbeatSuccessAge() < heartbeatGrace
			// Phase DD (v0.10.96): UI honesty axis. Without this gate,
			// a wedged watchNetworkChanges goroutine leaves the UI
			// reporting "connected" indefinitely while the data plane
			// is dead — heartbeat is on a separate goroutine and stays
			// fresh. Adding watcherAlive flips the indicator to
			// "connection issue" within ~30s of detection (the
			// watcher-watchdog interval) and stays false until the
			// auto-restart spawns a fresh watcher that begins stamping.
			//
			// Cold-start grace: the watcher is only spawned when Nebula
			// is up AND endpoint is non-empty (cmd/agent/main.go:611).
			// Bundled mode pre-attach, OS-stack fallback, or empty
			// endpoint enrollments never stamp — treat "never stamped"
			// as alive (no watcher to wedge). The 100-year threshold
			// matches runWatcherWatchdog's own cold-start exclusion.
			watcherAge := inst.watcherActivityAge()
			watcherAlive := watcherAge < heartbeatGrace || watcherAge > 100*365*24*time.Hour
			es.Connected = certValid && watcherAlive && (activeFlow || recentHeartbeat)
		}
	}

	return es
}

func (s *localAPIServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.buildStatus())
}

func (s *localAPIServer) handlePeers(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("enrollment")
	if name == "" {
		// If a single enrollment exists, default to it for convenience.
		list := s.enrolls().List()
		if len(list) == 1 {
			name = list[0].Name
		} else {
			writeJSONError(w, http.StatusBadRequest, "enrollment query param required")
			return
		}
	}
	inst := s.instances().get(name)
	if inst == nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("no live instance %q", name))
		return
	}
	ctrl := inst.control()
	if ctrl == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enrollment": name,
			"connected":  false,
			"direct":     0,
			"relayed":    0,
			"peers":      []PeerDetail{},
		})
		return
	}
	direct, relayed, peers, _ := collectPeerState(ctrl, inst.pathQuality)
	enriched := enrichPeersWithInfo(peers, inst)
	writeJSON(w, http.StatusOK, map[string]any{
		"enrollment": name,
		"connected":  true,
		"direct":     direct,
		"relayed":    relayed,
		"peers":      enriched,
	})
}

// peerDetailWithInfo is the response shape for /local/peers — embeds
// the protocol-level PeerDetail and adds the human-readable identifiers
// pulled from inst.peerInfoCache (populated by sendHeartbeat from the
// server's peerInfo response). Only this local-API endpoint needs the
// names; the heartbeat body and dashboard already have access to them
// directly through the control plane.
type peerDetailWithInfo struct {
	PeerDetail
	Name           string   `json:"name,omitempty"`
	DnsHostname    string   `json:"dnsHostname,omitempty"`
	CustomDnsNames []string `json:"customDnsNames,omitempty"`
	// OS is the peer's runtime.GOOS ("darwin", "linux", "windows").
	// Phase II.2 (v0.11.2): drives the per-peer OS label in the
	// desktop client's peer list.
	OS string `json:"os,omitempty"`
	// NodeID is the peer's server-side UUID. Phase II.3 (v0.11.3):
	// used by the desktop client's "Terminal" button to construct
	// the dashboard's per-node terminal URL.
	NodeID string `json:"nodeId,omitempty"`
	// IsLighthouse marks this peer as a lighthouse for the network.
	// Detected by checking the peer's vpnAddr against the
	// lighthouse.hosts list in the instance's nebula.yaml. Phase II.2:
	// the desktop client uses this to (a) label the row "Lighthouse"
	// instead of just "10.42.1.1", and (b) hide the Terminal button —
	// lighthouses are control-plane infrastructure, not user-operable
	// peers.
	IsLighthouse bool `json:"isLighthouse,omitempty"`
}

func enrichPeersWithInfo(peers []PeerDetail, inst *meshInstance) []peerDetailWithInfo {
	// Read the lighthouse address set once per /local/peers call.
	// readLighthouseAddrs is best-effort + fast (single yaml parse);
	// no caching needed for the call frequency this endpoint sees.
	lighthouses := readLighthouseAddrs(filepath.Join(inst.dir(), "nebula.yaml"))
	out := make([]peerDetailWithInfo, 0, len(peers))
	for _, p := range peers {
		entry := peerDetailWithInfo{PeerDetail: p}
		if v, ok := inst.peerInfoCache.Load(p.VpnAddr); ok {
			if info, ok2 := v.(peerInfoEntry); ok2 {
				entry.Name = info.Name
				entry.DnsHostname = info.DnsHostname
				entry.CustomDnsNames = info.CustomDnsNames
				entry.OS = info.OS
				entry.NodeID = info.NodeID
			}
		}
		if addr, err := netip.ParseAddr(p.VpnAddr); err == nil {
			if _, isLH := lighthouses[addr]; isLH {
				entry.IsLighthouse = true
			}
		}
		out = append(out, entry)
	}
	return out
}

func (s *localAPIServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Send a snapshot immediately so subscribers don't wait 5s for the
	// first heartbeat tick.
	snapshot := localEvent{Time: time.Now(), Type: "status", Data: s.statusSnapshot()}
	if err := writeSSE(w, snapshot); err != nil {
		return
	}
	flusher.Flush()

	ch := s.events.subscribe()
	defer s.events.unsubscribe(ch)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if err := writeSSE(w, ev); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// --- Enrollment handlers (Phase 1C) ---

// deviceFlowState tracks an in-progress device flow keyed by deviceCode.
type deviceFlowState struct {
	deviceCode string
	endpoint   string
	expiresAt  time.Time
	interval   time.Duration
	hostname   string
	tunMode    string
	name       string
}

var (
	deviceFlowMu     sync.Mutex
	activeDeviceFlow map[string]*deviceFlowState // keyed by deviceCode
)

func init() {
	activeDeviceFlow = make(map[string]*deviceFlowState)
}

type enrollDeviceFlowStartReq struct {
	Endpoint string `json:"endpoint"`
	Name     string `json:"name,omitempty"`
	TunMode  string `json:"tunMode,omitempty"`
}

type enrollDeviceFlowStartResp struct {
	DeviceCode      string `json:"deviceCode"`
	UserCode        string `json:"userCode"`
	VerificationURL string `json:"verificationUrl"`
	ExpiresIn       int    `json:"expiresIn"`
	Interval        int    `json:"interval"`
}

func (s *localAPIServer) handleEnrollDeviceFlowStart(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req enrollDeviceFlowStartReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	endpoint := strings.TrimSpace(req.Endpoint)
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		writeJSONError(w, http.StatusBadRequest, "endpoint must start with http:// or https://")
		return
	}
	if _, err := url.Parse(endpoint); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid endpoint URL")
		return
	}
	tunMode := req.TunMode
	if tunMode == "" {
		if isPrivileged() {
			tunMode = "kernel"
		} else {
			tunMode = "userspace"
		}
	}
	if tunMode != "kernel" && tunMode != "userspace" {
		writeJSONError(w, http.StatusBadRequest, "invalid tunMode")
		return
	}
	if name := strings.TrimSpace(req.Name); name != "" {
		if err := validateEnrollmentName(name); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// Clock-skew gate. EnsureClockSane probes the endpoint's HTTP Date
	// header and (on macOS root / Linux root) attempts an NTP resync if
	// skew exceeds 30 minutes. Non-root bundled-mode agents (Linux .deb
	// desktop, common case post-Stages-5+6) can't actually step the clock
	// — but we still need to detect the skew BEFORE the user goes to the
	// browser to approve, because the cert issued during /api/device/poll
	// will have NotBefore set to the server's "now". If the local clock
	// is N seconds behind, the cert won't be valid until N seconds elapse
	// — and Nebula refuses to load a not-yet-valid cert with the error
	// "nebula certificate for this host is expired" (it's actually
	// not-yet-valid, but Nebula's IsExpired() lumps both checks together).
	// Surface a clear, actionable error here instead of letting the user
	// complete onboarding and then fail at auto-connect with a misleading
	// "kernel TUN + userspace both failed" error.
	skewCtx, skewCancel := context.WithTimeout(r.Context(), 15*time.Second)
	skew := EnsureClockSane(skewCtx, endpoint)
	skewCancel()
	abs := skew
	if abs < 0 {
		abs = -abs
	}
	if abs > 60*time.Second {
		platformHint := "open System Settings → Date & Time and enable automatic time"
		switch runtime.GOOS {
		case "linux":
			platformHint = "run `sudo timedatectl set-ntp true` in a terminal, or check chrony/systemd-timesyncd"
		case "windows":
			platformHint = "open Settings → Time & Language → Date & Time and enable Set time automatically"
		}
		writeJSONError(w, http.StatusPreconditionFailed,
			fmt.Sprintf(
				"Your device clock is %s off from the server. The certificate issued during enrollment would be invalid until your clock catches up. To fix: %s, then try again.",
				skew.Round(time.Second), platformHint))
		return
	}

	postCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(postCtx, "POST", endpoint+"/api/device/code", nil)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := freshHTTPClient(30 * time.Second).Do(httpReq)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "device code request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("device code HTTP %d: %s", resp.StatusCode, body))
		return
	}
	var codeResp struct {
		DeviceCode              string `json:"deviceCode"`
		UserCode                string `json:"userCode"`
		ExpiresIn               int    `json:"expiresIn"`
		Interval                int    `json:"interval"`
		VerificationURIComplete string `json:"verificationURIComplete"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&codeResp); err != nil {
		writeJSONError(w, http.StatusBadGateway, "decode device code: "+err.Error())
		return
	}

	hostname, _ := os.Hostname()
	state := &deviceFlowState{
		deviceCode: codeResp.DeviceCode,
		endpoint:   endpoint,
		expiresAt:  time.Now().Add(time.Duration(codeResp.ExpiresIn) * time.Second),
		interval:   time.Duration(codeResp.Interval) * time.Second,
		hostname:   hostname,
		tunMode:    tunMode,
		name:       strings.TrimSpace(req.Name),
	}
	if state.interval < 2*time.Second {
		state.interval = 5 * time.Second
	}
	deviceFlowMu.Lock()
	activeDeviceFlow[codeResp.DeviceCode] = state
	deviceFlowMu.Unlock()

	// Prefer the server's verificationURIComplete (RFC 8628 — URL with the
	// user code embedded as ?code=). Falls back to the bare /device path
	// for older control planes that don't return the field — the page
	// itself accepts a manual code-entry path so both work.
	verificationURL := endpoint + "/device"
	if codeResp.VerificationURIComplete != "" {
		// Server returns a relative path (/device?code=HOP-XXXX); join it
		// to the endpoint base. Treat absolute URLs as-is for safety.
		if strings.HasPrefix(codeResp.VerificationURIComplete, "http://") || strings.HasPrefix(codeResp.VerificationURIComplete, "https://") {
			verificationURL = codeResp.VerificationURIComplete
		} else {
			verificationURL = endpoint + codeResp.VerificationURIComplete
		}
	}
	writeJSON(w, http.StatusOK, enrollDeviceFlowStartResp{
		DeviceCode:      codeResp.DeviceCode,
		UserCode:        codeResp.UserCode,
		VerificationURL: verificationURL,
		ExpiresIn:       codeResp.ExpiresIn,
		Interval:        codeResp.Interval,
	})
}

type enrollDeviceFlowPollReq struct {
	DeviceCode string `json:"deviceCode"`
}

type enrollDeviceFlowPollResp struct {
	Status     string `json:"status"`               // "pending" | "expired" | "complete" | "error"
	Message    string `json:"message,omitempty"`
	Enrollment string `json:"enrollment,omitempty"` // populated on "complete"
	// F5 (v0.10.85+): Connected reflects whether the auto-connect after a
	// successful enrollment actually brought the mesh up. Pre-fix the
	// agent always returned status="complete" even when connectFn failed,
	// causing the desktop UI to show "Connected" while the mesh was dead.
	// On status="complete", Connected=false means the cert is on disk and
	// the registry has the entry, but the mesh isn't actually up yet —
	// the desktop should surface a Retry CTA. ConnectError carries the
	// underlying reason for context.
	Connected    bool   `json:"connected,omitempty"`
	ConnectError string `json:"connectError,omitempty"`
}

func (s *localAPIServer) handleEnrollDeviceFlowPoll(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req enrollDeviceFlowPollReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	deviceFlowMu.Lock()
	state, ok := activeDeviceFlow[req.DeviceCode]
	deviceFlowMu.Unlock()
	if !ok {
		writeJSONError(w, http.StatusNotFound, "no active device flow with that code")
		return
	}
	if time.Now().After(state.expiresAt) {
		deviceFlowMu.Lock()
		delete(activeDeviceFlow, req.DeviceCode)
		deviceFlowMu.Unlock()
		writeJSON(w, http.StatusOK, enrollDeviceFlowPollResp{Status: "expired", Message: "device code expired"})
		return
	}

	// F2 (v0.10.85+): include the agent's known CA fingerprints so the
	// server can short-circuit with 409 BEFORE creating a node row when
	// this device is already enrolled in the would-be network. Optional
	// field — older servers ignore it.
	pollPayload := struct {
		DeviceCode         string   `json:"deviceCode"`
		Hostname           string   `json:"hostname"`
		OS                 string   `json:"os"`
		Arch               string   `json:"arch"`
		ExistingNetworkCAs []string `json:"existingNetworkCAs,omitempty"`
	}{
		DeviceCode:         state.deviceCode,
		Hostname:           state.hostname,
		OS:                 runtime.GOOS,
		Arch:               detectArch(),
		ExistingNetworkCAs: s.enrolls().CAFingerprints(),
	}
	pollBodyBytes, err := json.Marshal(pollPayload)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "encode poll body: "+err.Error())
		return
	}
	postCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(postCtx, "POST", state.endpoint+"/api/device/poll", strings.NewReader(string(pollBodyBytes)))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := freshHTTPClient(30 * time.Second).Do(httpReq)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "poll failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	// F2 conflict — server detected the agent already has this network.
	// Surface a structured user-facing error rather than a cryptic 409.
	if resp.StatusCode == http.StatusConflict {
		var conflict struct {
			Error       string `json:"error"`
			NetworkName string `json:"networkName"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&conflict)
		deviceFlowMu.Lock()
		delete(activeDeviceFlow, req.DeviceCode)
		deviceFlowMu.Unlock()
		msg := conflict.Error
		if msg == "" {
			msg = "this device is already enrolled in this network"
		}
		if conflict.NetworkName != "" {
			msg = fmt.Sprintf("%s — open %q from the home screen, or Leave it first to start fresh", msg, conflict.NetworkName)
		}
		writeJSON(w, http.StatusOK, enrollDeviceFlowPollResp{Status: "error", Message: msg})
		return
	}

	if resp.StatusCode == http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		status := strings.TrimSpace(string(body))
		if status == "authorization_pending" {
			writeJSON(w, http.StatusOK, enrollDeviceFlowPollResp{Status: "pending"})
			return
		}
		if status == "expired_token" {
			deviceFlowMu.Lock()
			delete(activeDeviceFlow, req.DeviceCode)
			deviceFlowMu.Unlock()
			writeJSON(w, http.StatusOK, enrollDeviceFlowPollResp{Status: "expired", Message: "device code expired"})
			return
		}
		writeJSON(w, http.StatusOK, enrollDeviceFlowPollResp{Status: "error", Message: status})
		return
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		writeJSON(w, http.StatusOK, enrollDeviceFlowPollResp{Status: "error", Message: string(body)})
		return
	}

	var er enrollResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		writeJSON(w, http.StatusOK, enrollDeviceFlowPollResp{Status: "error", Message: "decode: " + err.Error()})
		return
	}

	enrollName := state.name // must be a global var per existing code
	name, err := s.installEnrollment(&er, state.endpoint, state.tunMode, enrollName)
	if err != nil {
		writeJSON(w, http.StatusOK, enrollDeviceFlowPollResp{Status: "error", Message: err.Error()})
		return
	}

	deviceFlowMu.Lock()
	delete(activeDeviceFlow, req.DeviceCode)
	deviceFlowMu.Unlock()

	s.events.publish(localEvent{
		Time: time.Now(),
		Type: "enrollment.added",
		Data: map[string]any{"name": name, "endpoint": state.endpoint},
	})

	// Auto-connect: bring the new mesh up immediately. The user should
	// never have to restart the agent — the v1 macOS client and any
	// future GUI shell rely on this for a "click → connected" UX.
	// If connect fails (rare; bind error or transient lighthouse
	// unreach), we still return "complete" because the enrollment is
	// persisted; the cert-renewal loop will retry the bring-up later.
	// F5 (v0.10.85+): the response now carries Connected + ConnectError
	// so the desktop's Onboarding can surface a Retry CTA instead of
	// silently lying about "Connected" when the mesh failed to come up.
	connected := false
	connectErrMsg := ""
	{
		if err := s.client.connect(name); err != nil {
			connectErrMsg = err.Error()
			log.Printf("[local-api] auto-connect after enroll %q failed: %v (enrollment persisted; retry via POST /local/connect)", name, err)
		} else {
			connected = true
			s.events.publish(localEvent{
				Time: time.Now(),
				Type: "enrollment.connected",
				Data: map[string]any{"name": name},
			})
		}
	}

	writeJSON(w, http.StatusOK, enrollDeviceFlowPollResp{
		Status:       "complete",
		Enrollment:   name,
		Connected:    connected,
		ConnectError: connectErrMsg,
	})
}

type enrollTokenReq struct {
	Token    string `json:"token"`
	Endpoint string `json:"endpoint"`
	Name     string `json:"name,omitempty"`
	TunMode  string `json:"tunMode,omitempty"`
}

func (s *localAPIServer) handleEnrollToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req enrollTokenReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	endpoint := strings.TrimSpace(req.Endpoint)
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	tunMode := req.TunMode
	if tunMode == "" {
		if isPrivileged() {
			tunMode = "kernel"
		} else {
			tunMode = "userspace"
		}
	}

	hostname, _ := os.Hostname()
	// F2 (v0.10.85+): include the agent's known CA fingerprints — same
	// rationale as the device-flow poll above.
	enrollPayload := struct {
		Token              string   `json:"token"`
		Hostname           string   `json:"hostname"`
		OS                 string   `json:"os"`
		Arch               string   `json:"arch"`
		ExistingNetworkCAs []string `json:"existingNetworkCAs,omitempty"`
	}{
		Token:              req.Token,
		Hostname:           hostname,
		OS:                 runtime.GOOS,
		Arch:               detectArch(),
		ExistingNetworkCAs: s.enrolls().CAFingerprints(),
	}
	bodyBytes, err := json.Marshal(enrollPayload)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "encode enroll body: "+err.Error())
		return
	}
	postCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(postCtx, "POST", endpoint+"/api/enroll", strings.NewReader(string(bodyBytes)))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := freshHTTPClient(30 * time.Second).Do(httpReq)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		// F2 conflict — surface as a structured error to the desktop UI.
		var conflict struct {
			Error       string `json:"error"`
			NetworkName string `json:"networkName"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&conflict)
		msg := conflict.Error
		if msg == "" {
			msg = "this device is already enrolled in this network"
		}
		if conflict.NetworkName != "" {
			msg = fmt.Sprintf("%s — open %q from the home screen, or Leave it first to start fresh", msg, conflict.NetworkName)
		}
		writeJSONError(w, http.StatusConflict, msg)
		return
	}
	if resp.StatusCode != 200 {
		bodyResp, _ := io.ReadAll(resp.Body)
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, bodyResp))
		return
	}
	var er enrollResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		writeJSONError(w, http.StatusBadGateway, "decode: "+err.Error())
		return
	}

	name, err := s.installEnrollment(&er, endpoint, tunMode, strings.TrimSpace(req.Name))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.events.publish(localEvent{
		Time: time.Now(),
		Type: "enrollment.added",
		Data: map[string]any{"name": name, "endpoint": endpoint},
	})

	// Auto-connect — same rationale as the device-flow path.
	autoConnected := false
	{
		if err := s.client.connect(name); err != nil {
			log.Printf("[local-api] auto-connect after token-enroll %q failed: %v", name, err)
		} else {
			autoConnected = true
			s.events.publish(localEvent{
				Time: time.Now(),
				Type: "enrollment.connected",
				Data: map[string]any{"name": name},
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"enrollment": name, "connected": autoConnected})
}

// installEnrollment writes the certs to disk and registers the enrollment.
// Returns the canonical enrollment name (may differ from requestedName if
// auto-suffixing was applied due to collision).
//
// This is a non-interactive subset of the existing installCerts() in
// enroll.go — it handles the same response payload but never prompts and
// never starts a service. The local API caller is expected to invoke the
// new connect endpoint afterward to actually bring Nebula up.
//
// The agent must be RESTARTED for the new enrollment to come up; runServe
// only starts Nebula instances at boot. We surface that via the response
// + an event on the SSE bus so the UI can prompt the user.
func (s *localAPIServer) installEnrollment(er *enrollResponse, endpoint, tunMode, requestedName string) (string, error) {
	if er.NodeID == "" || er.NodeCert == "" || er.NodeKey == "" || er.AgentToken == "" {
		return "", fmt.Errorf("enrollment response missing required fields")
	}

	caHex := caFingerprint([]byte(er.CACert))

	// Collision check against the same network — matches installCerts().
	if existing := existingEnrollmentForNetwork(s.enrolls(), endpoint, caHex); existing != nil {
		return "", fmt.Errorf("this device is already enrolled in this network as %q (node %s); use leave or --force to replace", existing.Name, existing.NodeID)
	}

	preferred := defaultEnrollmentName(er.DNSDomain, caHex)
	name, err := chooseEnrollmentName(s.enrolls(), requestedName, preferred)
	if err != nil {
		return "", err
	}

	enrollDir := enrollmentDir(s.configDir(), name)
	if err := os.MkdirAll(enrollDir, 0700); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", enrollDir, err)
	}

	files := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{"ca.crt", []byte(er.CACert), 0644},
		{"node.crt", []byte(er.NodeCert), 0644},
		{"node.key", []byte(er.NodeKey), 0600},
		{"token", []byte(er.AgentToken), 0600},
		{"endpoint", []byte(endpoint), 0600},
		{"node-id", []byte(er.NodeID), 0600},
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(enrollDir, f.name), f.data, f.mode); err != nil {
			return "", fmt.Errorf("write %s: %w", f.name, err)
		}
	}

	listenPort := s.enrolls().NextAvailableListenPort(4242)
	serverHost := er.LighthouseHost
	if serverHost == "" {
		serverHost = extractHost(endpoint)
	}

	// writeNebulaConfig + writeDNSConfig log.Fatal on internal write
	// errors today. Acceptable risk: at this point we've already
	// successfully written the cert files, so a write-failure would be
	// unusual. If/when we refactor those into error-returning forms,
	// swap in here.
	writeNebulaConfig(enrollDir, er.ServerIP, serverHost, er.LighthousePort, tunMode, listenPort)
	writeDNSConfig(enrollDir, er.DNSDomain, serverHost, er.LighthousePort)

	enrollment := &Enrollment{
		Name:          name,
		NodeID:        er.NodeID,
		Endpoint:      endpoint,
		TunMode:       tunMode,
		CAFingerprint: caHex,
		DNSDomain:     er.DNSDomain,
		ListenPort:    listenPort,
		EnrolledAt:    time.Now().UTC(),
	}
	if err := s.enrolls().Add(enrollment); err != nil {
		// Roll back the cert files so a failed registry add doesn't
		// leave orphan disk state.
		_ = os.RemoveAll(enrollDir)
		return "", fmt.Errorf("add to registry: %w", err)
	}

	return name, nil
}

// --- Connect / Disconnect / Leave ---

func (s *localAPIServer) handleConnect(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("enrollment")
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "enrollment query param required")
		return
	}
	if e := s.enrolls().Get(name); e == nil {
		writeJSONError(w, http.StatusNotFound, "enrollment not found")
		return
	}
	if inst := s.instances().get(name); inst != nil && inst.control() != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "already-connected", "enrollment": name})
		return
	}
	if err := s.client.connect(name); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.events.publish(localEvent{
		Time: time.Now(),
		Type: "enrollment.connected",
		Data: map[string]any{"name": name},
	})
	writeJSON(w, http.StatusOK, map[string]any{"status": "connected", "enrollment": name})
}

func (s *localAPIServer) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("enrollment")
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "enrollment query param required")
		return
	}
	if inst := s.instances().get(name); inst == nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "already-disconnected", "enrollment": name})
		return
	}
	if err := s.client.disconnect(name); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.events.publish(localEvent{
		Time: time.Now(),
		Type: "enrollment.disconnected",
		Data: map[string]any{"name": name},
	})
	writeJSON(w, http.StatusOK, map[string]any{"status": "disconnected", "enrollment": name})
}

type leaveReq struct {
	Enrollment string `json:"enrollment"`
}

func (s *localAPIServer) handleLeave(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req leaveReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Also accept query-string fallback.
		req.Enrollment = r.URL.Query().Get("enrollment")
	}
	if req.Enrollment == "" {
		writeJSONError(w, http.StatusBadRequest, "enrollment required (body or query)")
		return
	}
	target := s.enrolls().Get(req.Enrollment)
	if target == nil {
		writeJSONError(w, http.StatusNotFound, "enrollment not found")
		return
	}

	// Disconnect the live mesh instance FIRST (if any). This stops
	// Nebula, the watcher, the portmap, and the per-instance HTTP
	// listener BEFORE we delete cert files from disk. Without this
	// ordering, the instance's renewal loop could attempt to read the
	// just-deleted cert files mid-operation and log spurious errors.
	if s.instances().get(target.Name) != nil {
		if err := s.client.disconnect(target.Name); err != nil {
			log.Printf("[local-api] leave: disconnect %q failed: %v (continuing with file cleanup)", target.Name, err)
		}
	}

	// Best-effort DNS cleanup (no-op on macOS/Linux/Windows if not
	// configured; safe to call always).
	if cfg := readDNSConfigForEnrollment(target); cfg != nil {
		stub := newMeshInstance(target)
		_ = platformCleanupDNS(stub.name(), cfg.Domain)
	}

	if err := s.enrolls().Remove(target.Name); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	subdir := enrollmentDir(s.configDir(), target.Name)
	if len(subdir) > 5 {
		_ = os.RemoveAll(subdir)
	}
	s.events.publish(localEvent{
		Time: time.Now(),
		Type: "enrollment.removed",
		Data: map[string]any{"name": target.Name},
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"removed":         target.Name,
		"restartRequired": false, // v0.10.34: live disconnect handles teardown
	})
}

// handleForceRenew triggers an inline cert renewal POST for one
// enrollment, bypassing the timer-driven loop. Used by the desktop
// client's "Force renew" button (Phase P4) — gives the user an
// in-app one-click recovery for the silent-renewal-death scenario
// the watchdog (Phase P2) is also designed to catch automatically.
//
// Rate-limited to 1 force-renew per 60s per enrollment to prevent
// accidental DoS of the control plane from a stuck-button click.
//
// POST /local/renew  { "enrollment": "home" }
//   200 { "certNotAfter": "...", "peersDirect": N, "peersRelayed": N }
//   429 too many requests
//   404 enrollment not found
//   500 renewal failed (with error string)
var forceRenewLastAt sync.Map // enrollment name -> time.Time

func (s *localAPIServer) handleForceRenew(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		Enrollment string `json:"enrollment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Enrollment == "" {
		writeJSONError(w, http.StatusBadRequest, "enrollment required")
		return
	}
	target := s.enrolls().Get(req.Enrollment)
	if target == nil {
		writeJSONError(w, http.StatusNotFound, "enrollment not found")
		return
	}

	// Rate limit. 1/min per enrollment, atomic CAS via sync.Map LoadOrStore.
	now := time.Now()
	if v, ok := forceRenewLastAt.Load(target.Name); ok {
		if t, ok := v.(time.Time); ok && now.Sub(t) < 60*time.Second {
			writeJSONError(w, http.StatusTooManyRequests, "force-renew rate-limited (60s window)")
			return
		}
	}
	forceRenewLastAt.Store(target.Name, now)

	inst := s.instances().get(target.Name)
	if inst == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "instance not running")
		return
	}

	// Reuses the timer-loop's renewCert path. Synchronous: blocks
	// until renewal completes (or fails). Caller's HTTP handler has
	// a 30s write timeout — renewal typically takes <2s.
	if err := renewCert(inst); err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("renewal failed: %v", err))
		return
	}
	// Read back the fresh cert NotAfter for the response.
	es := s.enrollmentStatus(target)
	writeJSON(w, http.StatusOK, map[string]any{
		"enrollment":    target.Name,
		"certNotAfter":  es.CertNotAfter,
		"certExpiresIn": es.CertExpiresIn,
		"peersDirect":   es.PeersDirect,
		"peersRelayed":  es.PeersRelayed,
	})
}

// handleClipboardSyncToggle flips Enrollment.ClipboardSync for one
// network. Persisted to enrollments.json. Toggle takes effect on the
// next agent restart — runtime hot-toggling is intentionally NOT
// supported in v1 to keep the lifecycle simple (otherwise we'd need
// to teardown / re-spawn the clipboardSync goroutine here, which
// also means importing context cancellation through the registry).
//
// POST /local/clipboard-sync
//
//	{ "enrollment": "home", "enabled": true }
func (s *localAPIServer) handleClipboardSyncToggle(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		Enrollment string `json:"enrollment"`
		Enabled    bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Enrollment == "" {
		writeJSONError(w, http.StatusBadRequest, "enrollment required")
		return
	}
	target := s.enrolls().Get(req.Enrollment)
	if target == nil {
		writeJSONError(w, http.StatusNotFound, "enrollment not found")
		return
	}

	if err := s.enrolls().SetClipboardSync(target.Name, req.Enabled); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.events.publish(localEvent{
		Time: time.Now(),
		Type: "enrollment.changed",
		Data: map[string]any{"name": target.Name, "field": "clipboardSync", "value": req.Enabled},
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"enrollment":      target.Name,
		"clipboardSync":   req.Enabled,
		"restartRequired": true,
	})
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeSSE(w io.Writer, ev localEvent) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", b)
	return err
}

// freshHTTPClient returns an http.Client with keepalives disabled —
// matches the architectural rule from CLAUDE.md (the v0.10.31 incident).
func freshHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
}
