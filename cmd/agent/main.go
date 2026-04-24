package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/slackhq/nebula/cert"
	"github.com/trustos/hopssh/internal/buildinfo"
	"github.com/trustos/hopssh/internal/nebulacfg"
	"gopkg.in/yaml.v3"

	netpprof "net/http/pprof"
)

const (
	// maxUploadSize is the max upload body size (100 MB).
	maxUploadSize = 100 << 20
	// safeUploadDir is the only directory uploads are allowed to write to.
	safeUploadDir = "/var/hop-agent/uploads"
)

// execCommand wraps exec.Command for use by enroll.go.
var execCommand = exec.Command

func main() {
	debug.SetGCPercent(400)

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "help", "--help", "-h":
			runHelp()
			return
		case "version", "--version":
			fmt.Printf("hop-agent %s (%s)\n", buildinfo.Version, buildinfo.Commit)
			return
		case "status":
			runStatus(os.Args[2:])
			return
		case "info":
			runInfo(os.Args[2:])
			return
		case "enroll":
			runEnroll(os.Args[2:])
			return
		case "serve":
			runServe(os.Args[2:])
			return
		case "install":
			runAgentInstall(os.Args[2:])
			return
		case "uninstall":
			runAgentUninstall(os.Args[2:])
			return
		case "update":
			runAgentUpdate(os.Args[2:])
			return
		case "restart":
			runRestart(os.Args[2:])
			return
		case "stop":
			runStop()
			return
		case "leave":
			runLeave(os.Args[2:])
			return
		case "client":
			runClient(os.Args[2:])
			return
		case "migration":
			runMigration(os.Args[2:])
			return
		default:
			fmt.Fprintf(os.Stderr, "Unknown command: %s\nRun 'hop-agent help' for usage.\n", os.Args[1])
			os.Exit(1)
		}
	}
	// Default: serve (backwards compatible with existing systemd units).
	runServe(os.Args[1:])
}

func runServe(args []string) {
	// Shutdown signalling. Set up first so the Windows service
	// handler can redirect logs before any log.Fatal path runs.
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	defer shutdownCancel()

	// If launched by Windows SCM, redirect logs + install the service
	// handler that bridges Stop/Shutdown into shutdownCancel. Returns
	// false in console mode; we then rely on signals.
	_ = svcIntegrateIfNeeded(shutdownCancel)

	// Clean up any leftover <exe>.old from a previous Windows self
	// update. No-op on other platforms.
	cleanupOldBinary()

	// Optional loopback-only pprof listener for development/profiling.
	// Activated only when HOPSSH_PPROF_ADDR is set (e.g. "127.0.0.1:6060").
	// Loopback-only by design — no auth, no exposure to the mesh or LAN.
	// Used to drive `go tool pprof` against a running agent without
	// having to fish the bearer token out of the per-enrollment subdir.
	startPprofIfRequested()

	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgDir := fs.String("config-dir", "", "Override config directory")
	tokenFile := fs.String("token-file", "", "Path to the bearer token file")
	token := fs.String("token", "", "Bearer token (overrides -token-file)")
	endpointFile := fs.String("endpoint-file", "", "Path to control plane endpoint URL")
	nodeIDFile := fs.String("node-id-file", "", "Path to node ID file")
	nebulaConfig := fs.String("nebula-config", "", "Path to Nebula config")
	listenAddr := fs.String("listen", "", "Override listen address (bypasses mesh, uses OS stack)")
	fs.Parse(args)

	if *cfgDir != "" {
		configDir = resolveConfigDir(*cfgDir)
	}

	// Migrate any pre-v0.10 flat layout into the new subdir layout
	// before reading anything else. Idempotent + safe on fresh installs.
	if _, err := migrateLegacyLayout(configDir); err != nil {
		log.Fatalf("Legacy config migration failed: %v", err)
	}

	reg, err := loadEnrollmentRegistry(configDir)
	if err != nil {
		log.Fatalf("Load enrollments: %v", err)
	}
	// Set activeEnrollment so CLI-style paths (readEndpointFromDisk
	// fallbacks, legacy file defaults) continue to work against one
	// "primary" enrollment. The per-instance runtime (below) does not
	// depend on this global.
	if reg.Len() > 0 {
		setActiveEnrollment(reg.List()[0])
	}

	// Honor legacy flag overrides (--token-file, --endpoint-file, etc.)
	// for un-enrolled debug runs. When enrollments exist, the per-
	// instance loop below reads paths directly off the instance's
	// subdir instead of these flags.
	baseDir := activeEnrollDir()
	if *tokenFile == "" {
		*tokenFile = filepath.Join(baseDir, "token")
	}
	if *endpointFile == "" {
		*endpointFile = filepath.Join(baseDir, "endpoint")
	}
	if *nodeIDFile == "" {
		*nodeIDFile = filepath.Join(baseDir, "node-id")
	}
	if *nebulaConfig == "" {
		*nebulaConfig = filepath.Join(baseDir, "nebula.yaml")
	}

	// Build the HTTP mux once — handlers are stateless except for the
	// per-instance auth token which gets wrapped per listener below.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("POST /exec", handleExec)
	mux.HandleFunc("POST /upload", handleUpload)
	mux.HandleFunc("GET /shell", handleShell)
	mux.HandleFunc("/proxy/", handleProxy)
	mux.HandleFunc("GET /debug/pprof/", netpprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", netpprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", netpprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", netpprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", netpprof.Trace)

	renewCtx, renewCancel := context.WithCancel(context.Background())
	defer renewCancel()

	instances := newInstanceRegistry()
	servers := newServerSet()
	defer servers.shutdownAll()

	// --listen overrides + no enrollment → OS-stack-only debug mode.
	// Preserve the historical single-process behavior for running the
	// agent against a manual --token for ad-hoc testing.
	if *listenAddr != "" && reg.Len() == 0 {
		if err := startDebugOSListener(servers, mux, *token, *tokenFile, *listenAddr); err != nil {
			log.Fatalf("%v", err)
		}
	} else if reg.Len() == 0 {
		// No enrollment and no explicit listen → serve on mesh-less OS
		// stack at the default port so `hop-agent enroll` workflows that
		// expect an already-running process still succeed.
		log.Printf("[agent] no enrollments found, running on OS stack (enroll with 'hop-agent enroll')")
		if err := startDebugOSListener(servers, mux, *token, *tokenFile, fmt.Sprintf(":%d", agentAPIPort)); err != nil {
			log.Fatalf("%v", err)
		}
	} else {
		// Migrate legacy enrollments missing a per-enrollment listen
		// port. Pre-v0.10.3 enrollments shared port 4242 (with the
		// non-primary one falling back to a random ephemeral port at
		// runtime, breaking NAT mappings on every restart). Assign
		// each a unique deterministic port + persist + heal nebula.yaml.
		migrateListenPorts(reg)

		// The common case: start one Nebula instance per enrollment.
		for _, e := range reg.List() {
			inst := newMeshInstance(e)
			instances.add(inst)
			startMeshInstance(renewCtx, inst, servers, mux)
		}
	}

	// Wait for Unix signals (SIGINT/SIGTERM) OR Windows SCM
	// Stop/Shutdown — both cancel shutdownCtx via shutdownCancel set
	// up at the top of runServe.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		select {
		case <-sig:
			shutdownCancel()
		case <-shutdownCtx.Done():
		}
	}()

	<-shutdownCtx.Done()

	log.Println("Shutting down agent...")
	renewCancel()
	servers.shutdownAll()
	instances.closeAll()
}

// migrateListenPorts handles two related issues introduced before
// per-enrollment listen ports landed:
//
//  1. Pre-existing enrollments lack the ListenPort field — assign each
//     a unique port starting at nebulacfg.ListenPort + persist.
//  2. The on-disk nebula.yaml may carry the legacy port (4242 for the
//     primary, 0 for the rest) — overwrite listen.port to match the
//     newly-assigned ListenPort.
//
// Without this fix, multiple enrollments race for the same UDP port
// at boot and the loser falls back to a random ephemeral port (port 0)
// — which breaks NAT-PMP mapping reuse, breaks lighthouse host updates
// across restarts, and leaves the slow path unable to establish tunnels.
func migrateListenPorts(reg *enrollmentRegistry) {
	updated, err := reg.AssignMissingListenPorts(nebulacfg.ListenPort)
	if err != nil {
		log.Printf("[migrate] WARNING: failed to assign listen ports: %v", err)
		return
	}
	if updated > 0 {
		log.Printf("[migrate] assigned listen ports to %d legacy enrollment(s)", updated)
	}
	for _, e := range reg.List() {
		if err := healListenPortYAML(e); err != nil {
			log.Printf("[migrate %s] WARNING: heal listen.port: %v", e.Name, err)
		}
	}
}

// healListenPortYAML rewrites listen.port in this enrollment's
// nebula.yaml if it doesn't match the persisted ListenPort.
// Idempotent — no-op if already in sync.
func healListenPortYAML(e *Enrollment) error {
	cfgPath := filepath.Join(enrollmentDir(configDir, e.Name), "nebula.yaml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return err
	}
	listen, _ := cfg["listen"].(map[string]any)
	if listen == nil {
		listen = map[string]any{"host": "0.0.0.0"}
	}
	curPort, _ := listen["port"].(int)
	if curPort == e.ListenPort {
		return nil
	}
	listen["port"] = e.ListenPort
	cfg["listen"] = listen
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(cfgPath, out, 0644); err != nil {
		return err
	}
	log.Printf("[migrate %s] nebula.yaml listen.port updated %d → %d", e.Name, curPort, e.ListenPort)
	return nil
}

// startMeshInstance brings up Nebula + heartbeat + renewal + DNS for one
// enrollment and wires a per-instance HTTP server onto its mesh listener.
// Best-effort: on Nebula failure we fall back to an OS-stack listener
// scoped to this instance so the renewal loop can still reach the
// control plane and recover later.
func startMeshInstance(ctx context.Context, inst *meshInstance, servers *serverSet, mux http.Handler) {
	cfgPath := filepath.Join(inst.dir(), "nebula.yaml")
	inst.parentCtx = ctx

	authToken, err := readInstanceToken(inst)
	if err != nil {
		log.Fatalf("[agent %s] read token: %v", inst.name(), err)
	}
	authed := authMiddleware(authToken, mux)

	// Start cert renewal + heartbeat regardless of Nebula outcome —
	// even an expired-cert agent needs to renew + re-sync.
	if inst.endpoint() != "" && inst.nodeID() != "" {
		go runCertRenewal(ctx, inst)
		go runHeartbeat(ctx, inst)
		log.Printf("[agent %s] cert auto-renewal + heartbeat enabled (endpoint: %s)", inst.name(), inst.endpoint())
	}

	// If nebula.yaml is missing, fall back to OS stack (rare — should
	// only happen if enrollment is corrupt). Renewal might recover it.
	if _, err := os.Stat(cfgPath); err != nil {
		log.Printf("[agent %s] no Nebula config at %s, running on OS stack", inst.name(), cfgPath)
		servers.startOSListener(inst, authed, fmt.Sprintf(":%d", agentAPIPort))
		return
	}

	tunMode := readTunMode(inst)
	ensureP2PConfig(inst)

	inst.onRestart = func(newSvc meshService) {
		if err := servers.rebindMesh(inst, authed, newSvc); err != nil {
			log.Printf("[agent %s] CRITICAL: cannot listen on new Nebula instance: %v", inst.name(), err)
		}
	}

	meshSvc := startMesh(cfgPath, tunMode)
	if meshSvc == nil {
		log.Printf("[agent %s] all Nebula modes failed — falling back to OS stack", inst.name())
		servers.startOSListener(inst, authed, fmt.Sprintf(":%d", agentAPIPort))
		return
	}
	inst.setSvc(meshSvc)
	log.Printf("[agent %s] Nebula mesh connected (mode: %s)", inst.name(), tunMode)

	// Configure split-DNS for this mesh's domain in kernel TUN mode.
	if tunMode == "kernel" {
		inst.dnsConfig = readDNSConfig(inst)
		configureDNS(inst, inst.dnsConfig)
	}

	// Warm tunnels synchronously: lighthouse first, then peers from
	// heartbeat. Both must complete before the mesh listener starts,
	// otherwise Screen Sharing's quality probe fails on first connect.
	warmTunnel(cfgPath)
	warmPeersFromHeartbeat(inst, inst.endpoint())

	if ctrl := meshSvc.NebulaControl(); ctrl != nil {
		inst.startWatcher(ctrl)
	}

	if nebulacfg.PortmapEnabled {
		port := inst.enrollment.ListenPort
		if port == 0 {
			port = nebulacfg.ListenPort // fallback for pre-migration runs
		}
		inst.startPortmap(ctx, uint16(port))
	}

	if err := servers.startMeshListener(inst, authed, meshSvc, fmt.Sprintf(":%d", agentAPIPort)); err != nil {
		log.Fatalf("[agent %s] Nebula mesh listen: %v", inst.name(), err)
	}
	log.Printf("[agent %s] listening on :%d (Nebula mesh, %s TUN)", inst.name(), agentAPIPort, tunMode)
}

// startDebugOSListener serves the mux directly on the OS stack using a
// bearer token from --token or --token-file. Used only when no
// enrollment exists — mainly for ad-hoc local testing.
func startDebugOSListener(servers *serverSet, mux http.Handler, tokenFlag, tokenFilePath, listenAddr string) error {
	authToken := tokenFlag
	if authToken == "" && tokenFilePath != "" {
		data, err := os.ReadFile(tokenFilePath)
		if err != nil {
			return fmt.Errorf("cannot read token file %s: %w", tokenFilePath, err)
		}
		authToken = strings.TrimSpace(string(data))
	}
	if authToken == "" {
		return fmt.Errorf("no authentication token configured (pass --token or --token-file, or enroll first)")
	}
	authed := authMiddleware(authToken, mux)
	return servers.startUnscopedOSListener(authed, listenAddr)
}

// startPMTUD is disabled — requires fork with PMTUD support.
// func startPMTUD(ctx context.Context, ctrl *nebula.Control, configPath string) { ... }

// warmTunnel blocks until Noise handshakes complete to all reachable mesh
// peers. The TCP dials go through the TUN device, triggering Nebula handshakes.
// DialTimeout blocks until the handshake + TCP round-trip succeeds, so when
// this function returns, all peer tunnels are warm.
func warmTunnel(configPath string) {
	time.Sleep(500 * time.Millisecond)

	dir := filepath.Dir(configPath)
	certPEM, err := os.ReadFile(filepath.Join(dir, "node.crt"))
	if err != nil {
		return
	}
	c, _, err := cert.UnmarshalCertificateFromPEM(certPEM)
	if err != nil {
		return
	}
	networks := c.Networks()
	if len(networks) == 0 {
		return
	}

	lighthouseAddr := networks[0].Masked().Addr().Next()
	start := time.Now()

	d := net.Dialer{Timeout: 5 * time.Second}
	if conn, err := d.DialContext(context.Background(), "tcp", net.JoinHostPort(lighthouseAddr.String(), "41820")); err == nil {
		conn.Close()
	}
	log.Printf("[agent] warm-up: lighthouse ready in %s", time.Since(start).Truncate(time.Millisecond))
}

// warmPeersFromHeartbeat sends a heartbeat to get online peer IPs, then
// dials each one to establish Nebula tunnels before accepting connections.
func warmPeersFromHeartbeat(inst *meshInstance, endpoint string) {
	dir := inst.dir()
	nodeID, _ := os.ReadFile(filepath.Join(dir, "node-id"))
	token, _ := os.ReadFile(filepath.Join(dir, "token"))
	if len(nodeID) == 0 || len(token) == 0 {
		return
	}

	reqBody := fmt.Sprintf(`{"nodeId":%q}`, strings.TrimSpace(string(nodeID)))
	req, err := http.NewRequest("POST", endpoint+"/api/heartbeat", strings.NewReader(reqBody))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return
	}
	defer resp.Body.Close()

	var body struct {
		Peers         []string            `json:"peers"`
		PeerEndpoints map[string][]string `json:"peerEndpoints"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return
	}

	// Inject advertised peer UDP endpoints into Nebula's hostmap BEFORE
	// dialing, so the TCP dials below traverse an already-populated hostmap
	// (direct handshake path) instead of falling through to lighthouse
	// discovery (which may be unreachable on carrier-filtered cellular).
	if len(body.PeerEndpoints) > 0 {
		injectPeerEndpoints(inst, body.PeerEndpoints)
	}

	if len(body.Peers) == 0 {
		return
	}
	start := time.Now()
	for _, ip := range body.Peers {
		d := net.Dialer{Timeout: 2 * time.Second}
		if conn, err := d.Dial("tcp", net.JoinHostPort(ip, "41820")); err == nil {
			conn.Close()
		}
	}
	log.Printf("[agent] warm-up: %d peers ready in %s", len(body.Peers), time.Since(start).Truncate(time.Millisecond))
}

// startMesh starts Nebula in the requested TUN mode with graceful fallback.
// Tries kernel TUN first (if requested), falls back to userspace, returns nil if all fail.
func startMesh(configPath, tunMode string) meshService {
	if tunMode == "kernel" {
		if err := ensureWinTun(); err != nil {
			log.Printf("[agent] WARNING: wintun setup failed: %v", err)
		}
		svc, err := startNebulaKernelTun(configPath)
		if err != nil {
			log.Printf("[agent] WARNING: kernel TUN failed: %v (falling back to userspace)", err)
			// Fall through to userspace.
		} else {
			return svc
		}
	}

	svc, err := startNebula(configPath)
	if err != nil {
		log.Printf("[agent] WARNING: Nebula userspace failed: %v (falling back to OS stack)", err)
		return nil
	}
	return svc
}

func authMiddleware(token string, next http.Handler) http.Handler {
	expected := "Bearer " + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if subtle.ConstantTimeCompare([]byte(auth), []byte(expected)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- Health endpoint ---

type healthResponse struct {
	Status   string `json:"status"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Uptime   string `json:"uptime,omitempty"`
}

var startTime = time.Now()

func handleHealth(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()
	resp := healthResponse{
		Status:   "ok",
		Hostname: hostname,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Uptime:   time.Since(startTime).Truncate(time.Second).String(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// --- Exec endpoint ---

type execRequest struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Dir     string   `json:"dir,omitempty"`
	Env     []string `json:"env,omitempty"`
}

func handleExec(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req execRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Command == "" {
		http.Error(w, "command is required", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, req.Command, req.Args...)
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}
	if len(req.Env) > 0 {
		cmd.Env = append(os.Environ(), req.Env...)
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(w, "ERROR: %v\n", err)
		flusher.Flush()
		return
	}

	go func() {
		cmd.Wait()
		pw.Close()
	}()

	buf := make([]byte, 4096)
	for {
		n, err := pr.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			flusher.Flush()
		}
		if err != nil {
			break
		}
	}

	exitCode := 0
	if cmd.ProcessState != nil && !cmd.ProcessState.Success() {
		exitCode = cmd.ProcessState.ExitCode()
	}
	fmt.Fprintf(w, "\n---EXIT:%d---\n", exitCode)
	flusher.Flush()
}

// --- Upload endpoint ---

func handleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)

	destPath := r.Header.Get("X-Dest-Path")
	if destPath == "" {
		http.Error(w, "X-Dest-Path header is required", http.StatusBadRequest)
		return
	}

	// Restrict writes to the safe upload directory to prevent arbitrary file overwrites.
	cleanPath := filepath.Clean(destPath)
	if !strings.HasPrefix(cleanPath, safeUploadDir+"/") && cleanPath != safeUploadDir {
		http.Error(w, fmt.Sprintf("uploads are restricted to %s", safeUploadDir), http.StatusForbidden)
		return
	}

	modeStr := r.Header.Get("X-File-Mode")
	mode := os.FileMode(0644)
	if modeStr != "" {
		var m uint32
		if _, err := fmt.Sscanf(modeStr, "%o", &m); err == nil {
			mode = os.FileMode(m)
		}
	}

	if err := os.MkdirAll(filepath.Dir(cleanPath), 0755); err != nil {
		http.Error(w, "mkdir: "+err.Error(), http.StatusInternalServerError)
		return
	}

	f, err := os.OpenFile(cleanPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		http.Error(w, "create file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()

	n, err := io.Copy(f, r.Body)
	if err != nil {
		http.Error(w, "write file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"path":  cleanPath,
		"bytes": n,
	})
}

// --- Shell endpoint (interactive WebSocket terminal) ---

var wsUpgrader = websocket.Upgrader{
	// The agent is not directly exposed to browsers — it sits behind the
	// Nebula mesh and the control plane's WebSocket proxy, which validates
	// origins before relaying. The auth middleware has already validated the
	// bearer token by the time this runs, so all origins are accepted.
	CheckOrigin: func(r *http.Request) bool { return true },
}

const shellResizePrefix = 1
