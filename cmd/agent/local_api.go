package main

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
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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
type localAPIServer struct {
	configDir string
	enrolls   *enrollmentRegistry
	instances *instanceRegistry
	token     string

	// connectFn brings the named enrollment up: creates its meshInstance,
	// adds to the registry, starts Nebula + heartbeat + renewal + DNS +
	// HTTP listener. Used by both the explicit POST /local/connect path
	// and the auto-connect-after-enroll path. Set by runServe via
	// closure capture so the callback has access to runServe-local state
	// (servers, mux, renewCtx).
	//
	// Returns a typed error code as a string ("already-connected",
	// "ok", or a free-form error message) so the HTTP layer can
	// translate to an appropriate response status.
	connectFn func(name string) error

	// disconnectFn tears the named enrollment's mesh instance down:
	// stops Nebula, watcher, portmap, DNS, HTTP listener, removes from
	// the instance registry. The enrollment stays on disk and in the
	// enrollmentRegistry; only the live mesh state is dropped.
	disconnectFn func(name string) error

	// events is a fan-out hub for SSE subscribers.
	events *localEventHub
}

// localEvent is one entry on the SSE stream.
type localEvent struct {
	Time time.Time      `json:"time"`
	Type string         `json:"type"` // "status" | "peers" | "error" | "log"
	Data map[string]any `json:"data"`
}

// localEventHub is a tiny pub/sub for SSE subscribers. Each client gets
// its own buffered channel; if a subscriber falls behind beyond the buffer,
// we drop the event for that client (never block the producer).
type localEventHub struct {
	mu          sync.Mutex
	subscribers map[chan localEvent]struct{}
}

// globalEventBus is set by startLocalAPI() so other parts of the agent
// (the watchdog in keepalive.go, the connect/disconnect callbacks in
// main.go) can publish events without holding a reference to the
// localAPIServer struct. Nil if startLocalAPI hasn't been called yet
// or failed — publishToEventBus is then a silent no-op.
var globalEventBus atomic.Pointer[localEventHub]

// publishToEventBus emits an event on the package-level bus if one
// has been initialized. Safe to call from any goroutine; never blocks.
func publishToEventBus(ev localEvent) {
	if hub := globalEventBus.Load(); hub != nil {
		hub.publish(ev)
	}
}

func newLocalEventHub() *localEventHub {
	return &localEventHub{subscribers: make(map[chan localEvent]struct{})}
}

func (h *localEventHub) subscribe() chan localEvent {
	ch := make(chan localEvent, 32)
	h.mu.Lock()
	h.subscribers[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *localEventHub) unsubscribe(ch chan localEvent) {
	h.mu.Lock()
	delete(h.subscribers, ch)
	h.mu.Unlock()
	close(ch)
}

func (h *localEventHub) publish(ev localEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subscribers {
		select {
		case ch <- ev:
		default:
		}
	}
}

// startLocalAPI wires up the loopback HTTP API on 127.0.0.1:0, generates
// a fresh bearer token, persists it, prints HOPSSH_LOCAL_API:<addr>:<token>
// to stdout, and serves until ctx is cancelled. Non-fatal — returns the
// listener so callers can probe readiness, and an error on bind failure.
//
// connectFn / disconnectFn are required for live add/remove of mesh
// instances at runtime (POST /local/connect, /local/disconnect, and
// auto-connect after a successful enrollment). The runServe entry
// constructs them as closures over its servers + mux + renewCtx.
func startLocalAPI(
	ctx context.Context,
	cfgDir string,
	enrolls *enrollmentRegistry,
	instances *instanceRegistry,
	connectFn func(name string) error,
	disconnectFn func(name string) error,
) error {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		// crypto/rand failure means the system entropy source is broken.
		// Per coding principles: panic — nothing is safe.
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	token := hex.EncodeToString(tokenBytes)

	srv := &localAPIServer{
		configDir:    cfgDir,
		enrolls:      enrolls,
		instances:    instances,
		token:        token,
		connectFn:    connectFn,
		disconnectFn: disconnectFn,
		events:       newLocalEventHub(),
	}
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
}

// LocalStatus is the GET /local/status response.
type LocalStatus struct {
	Version         string             `json:"version"`
	Commit          string             `json:"commit"`
	OS              string             `json:"os"`
	Arch            string             `json:"arch"`
	ConfigDir       string             `json:"configDir"`
	ServiceStatus   string             `json:"serviceStatus,omitempty"`
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
	out := LocalStatus{
		Version:       buildinfo.Version,
		Commit:        buildinfo.Commit,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		ConfigDir:     s.configDir,
		ServiceStatus: readServiceStatus(),
		RunMode:       deriveRunMode(s.configDir),
		Enrollments:   []EnrollmentStatus{},
	}
	for _, e := range s.enrolls.List() {
		out.Enrollments = append(out.Enrollments, s.enrollmentStatus(e))
	}
	if pi := detectParallelInstall(s.configDir); pi != nil {
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
		Name:       e.Name,
		Endpoint:   e.Endpoint,
		NodeID:     e.NodeID,
		DNSDomain:  e.DNSDomain,
		TunMode:    e.TunMode,
		ListenPort: e.ListenPort,
	}

	// Cert info (best-effort).
	certPath := filepath.Join(enrollmentDir(s.configDir, e.Name), "node.crt")
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
	if inst := s.instances.get(e.Name); inst != nil {
		// Runtime TUN mode may differ from enroll-time. The agent
		// auto-upgrades userspace→kernel when started as root; that
		// upgrade writes the tun-mode file but NOT enrollments.json,
		// so reading e.TunMode lies. Override with runtime truth.
		es.TunMode = currentTunMode(inst)
		ctrl := inst.control()
		if ctrl != nil {
			es.Connected = true
			direct, relayed, _, ok := collectPeerState(ctrl, inst.pathQuality)
			if ok {
				es.PeersDirect = direct
				es.PeersRelayed = relayed
			}
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
		list := s.enrolls.List()
		if len(list) == 1 {
			name = list[0].Name
		} else {
			writeJSONError(w, http.StatusBadRequest, "enrollment query param required")
			return
		}
	}
	inst := s.instances.get(name)
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
}

func enrichPeersWithInfo(peers []PeerDetail, inst *meshInstance) []peerDetailWithInfo {
	out := make([]peerDetailWithInfo, 0, len(peers))
	for _, p := range peers {
		entry := peerDetailWithInfo{PeerDetail: p}
		if v, ok := inst.peerInfoCache.Load(p.VpnAddr); ok {
			if info, ok2 := v.(peerInfoEntry); ok2 {
				entry.Name = info.Name
				entry.DnsHostname = info.DnsHostname
				entry.CustomDnsNames = info.CustomDnsNames
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

	pollBody := fmt.Sprintf(`{"deviceCode":%q,"hostname":%q,"os":%q,"arch":%q}`,
		state.deviceCode, state.hostname, runtime.GOOS, detectArch())
	postCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(postCtx, "POST", state.endpoint+"/api/device/poll", strings.NewReader(pollBody))
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
	if s.connectFn != nil {
		if err := s.connectFn(name); err != nil {
			log.Printf("[local-api] auto-connect after enroll %q failed: %v (enrollment persisted; retry via POST /local/connect)", name, err)
		} else {
			s.events.publish(localEvent{
				Time: time.Now(),
				Type: "enrollment.connected",
				Data: map[string]any{"name": name},
			})
		}
	}

	writeJSON(w, http.StatusOK, enrollDeviceFlowPollResp{Status: "complete", Enrollment: name})
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
	body := fmt.Sprintf(`{"token":%q,"hostname":%q,"os":%q,"arch":%q}`,
		req.Token, hostname, runtime.GOOS, detectArch())
	postCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(postCtx, "POST", endpoint+"/api/enroll", strings.NewReader(body))
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
	if resp.StatusCode != 200 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, bodyBytes))
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
	if s.connectFn != nil {
		if err := s.connectFn(name); err != nil {
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
	if existing := existingEnrollmentForNetwork(s.enrolls, endpoint, caHex); existing != nil {
		return "", fmt.Errorf("this device is already enrolled in this network as %q (node %s); use leave or --force to replace", existing.Name, existing.NodeID)
	}

	preferred := defaultEnrollmentName(er.DNSDomain, caHex)
	name, err := chooseEnrollmentName(s.enrolls, requestedName, preferred)
	if err != nil {
		return "", err
	}

	enrollDir := enrollmentDir(s.configDir, name)
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

	listenPort := s.enrolls.NextAvailableListenPort(4242)
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
	if err := s.enrolls.Add(enrollment); err != nil {
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
	if e := s.enrolls.Get(name); e == nil {
		writeJSONError(w, http.StatusNotFound, "enrollment not found")
		return
	}
	if inst := s.instances.get(name); inst != nil && inst.control() != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "already-connected", "enrollment": name})
		return
	}
	if s.connectFn == nil {
		writeJSONError(w, http.StatusInternalServerError, "live connect not wired (connectFn nil)")
		return
	}
	if err := s.connectFn(name); err != nil {
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
	if s.disconnectFn == nil {
		writeJSONError(w, http.StatusInternalServerError, "live disconnect not wired (disconnectFn nil)")
		return
	}
	if inst := s.instances.get(name); inst == nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "already-disconnected", "enrollment": name})
		return
	}
	if err := s.disconnectFn(name); err != nil {
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
	target := s.enrolls.Get(req.Enrollment)
	if target == nil {
		writeJSONError(w, http.StatusNotFound, "enrollment not found")
		return
	}

	// Disconnect the live mesh instance FIRST (if any). This stops
	// Nebula, the watcher, the portmap, and the per-instance HTTP
	// listener BEFORE we delete cert files from disk. Without this
	// ordering, the instance's renewal loop could attempt to read the
	// just-deleted cert files mid-operation and log spurious errors.
	if s.disconnectFn != nil && s.instances.get(target.Name) != nil {
		if err := s.disconnectFn(target.Name); err != nil {
			log.Printf("[local-api] leave: disconnect %q failed: %v (continuing with file cleanup)", target.Name, err)
		}
	}

	// Best-effort DNS cleanup (no-op on macOS/Linux/Windows if not
	// configured; safe to call always).
	if cfg := readDNSConfigForEnrollment(target); cfg != nil {
		stub := newMeshInstance(target)
		_ = platformCleanupDNS(stub.name(), cfg.Domain)
	}

	if err := s.enrolls.Remove(target.Name); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	subdir := enrollmentDir(s.configDir, target.Name)
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
