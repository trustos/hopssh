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
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/slackhq/nebula/cert"
	"github.com/trustos/hopssh/internal/buildinfo"

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
			runRestart()
			return
		case "stop":
			runStop()
			return
		case "client":
			runClient(os.Args[2:])
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
	// Derive defaults from configDir if not explicitly set.
	if *tokenFile == "" {
		*tokenFile = filepath.Join(configDir, "token")
	}
	if *endpointFile == "" {
		*endpointFile = filepath.Join(configDir, "endpoint")
	}
	if *nodeIDFile == "" {
		*nodeIDFile = filepath.Join(configDir, "node-id")
	}
	if *nebulaConfig == "" {
		*nebulaConfig = filepath.Join(configDir, "nebula.yaml")
	}

	authToken := *token
	if authToken == "" {
		data, err := os.ReadFile(*tokenFile)
		if err != nil {
			log.Fatalf("Cannot read token file %s: %v", *tokenFile, err)
		}
		authToken = strings.TrimSpace(string(data))
	}
	if authToken == "" {
		log.Fatal("No authentication token configured")
	}

	// Build HTTP handler once — it never changes across Nebula restarts.
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
	authed := authMiddleware(authToken, mux)

	// Read endpoint for config management and cert renewal.
	var agentEndpoint string
	if epData, err := os.ReadFile(*endpointFile); err == nil {
		agentEndpoint = strings.TrimSpace(string(epData))
	}

	// Start cert renewal + heartbeat if endpoint + nodeID are available.
	renewCtx, renewCancel := context.WithCancel(context.Background())
	defer renewCancel()
	if agentEndpoint != "" {
		if idData, err := os.ReadFile(*nodeIDFile); err == nil {
			nodeID := strings.TrimSpace(string(idData))
			if nodeID != "" {
				go runCertRenewal(renewCtx, agentEndpoint, nodeID, authToken)
				go runHeartbeat(renewCtx, agentEndpoint, nodeID, authToken)
				log.Printf("[agent] cert auto-renewal + heartbeat enabled (endpoint: %s)", agentEndpoint)
			}
		}
	}

	// serveMu protects srv for concurrent access from reload callback + shutdown.
	var serveMu sync.Mutex
	newServer := func() *http.Server {
		return &http.Server{
			Handler:           authed,
			ReadTimeout:       30 * time.Second,
			ReadHeaderTimeout: 10 * time.Second,
			WriteTimeout:      0, // streaming responses for /exec and /shell
		}
	}
	srv := newServer()

	// DNS config for cleanup on shutdown.
	var activeDNSConfig *dnsConfig

	startOSListener := func() {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", agentAPIPort))
		if err != nil {
			log.Fatalf("Listen :%d: %v", agentAPIPort, err)
		}
		log.Printf("hop-agent listening on %s (OS stack)", ln.Addr())
		go func() {
			if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
				log.Fatalf("Serve: %v", err)
			}
		}()
	}

	if *listenAddr != "" {
		// Explicit --listen override: use OS network stack directly.
		ln, err := net.Listen("tcp", *listenAddr)
		if err != nil {
			log.Fatalf("Listen %s: %v", *listenAddr, err)
		}
		log.Printf("hop-agent listening on %s (OS stack override)", ln.Addr())
		// Still start Nebula for outbound mesh connectivity.
		if _, err := os.Stat(*nebulaConfig); err == nil {
			if svc, err := startNebula(*nebulaConfig); err == nil {
				nebulaMu.Lock()
				currentNebula = svc
				nebulaMu.Unlock()
				log.Printf("[agent] Nebula mesh connected (outbound only)")
			}
		}
		go func() {
			if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
				log.Fatalf("Serve: %v", err)
			}
		}()
	} else if _, err := os.Stat(*nebulaConfig); err == nil {
		// Start Nebula mesh. Auto-detect TUN mode from persisted config.
		tunMode := readTunMode()
		ensureP2PConfig(agentEndpoint)
		meshSvc := startMesh(*nebulaConfig, tunMode)

		if meshSvc == nil {
			// All Nebula modes failed — fall back to OS stack.
			startOSListener()
		} else {
			nebulaMu.Lock()
			currentNebula = meshSvc
			nebulaMu.Unlock()
			log.Printf("[agent] Nebula mesh connected (mode: %s)", tunMode)

			// Configure split-DNS for mesh domain in kernel TUN mode.
			if tunMode == "kernel" {
				activeDNSConfig = readDNSConfig()
				configureDNS(activeDNSConfig)
			}

			// Warm tunnels synchronously: lighthouse first, then peers from
			// heartbeat. Both must complete before the mesh listener starts,
			// otherwise Screen Sharing's quality probe fails on first connect.
			warmTunnel(*nebulaConfig)
			warmPeersFromHeartbeat(agentEndpoint)

			// Watch for network changes (WiFi↔cellular) and rebind Nebula.
			if ctrl := meshSvc.NebulaControl(); ctrl != nil {
				go watchNetworkChanges(ctrl, agentEndpoint)
			}

			meshLn, err := meshSvc.Listen("tcp", fmt.Sprintf(":%d", agentAPIPort))
			if err != nil {
				log.Fatalf("Nebula mesh listen: %v", err)
			}
			log.Printf("hop-agent listening on :%d (Nebula mesh, %s TUN)", agentAPIPort, tunMode)

			go func() {
				if err := srv.Serve(meshLn); err != nil && err != http.ErrServerClosed {
					log.Fatalf("Serve: %v", err)
				}
			}()

			// Register callback for Nebula restarts (cert renewal).
			onNebulaRestart = func(newSvc meshService) {
				serveMu.Lock()
				defer serveMu.Unlock()

				shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
				srv.Shutdown(shutCtx)
				shutCancel()

				newLn, err := newSvc.Listen("tcp", fmt.Sprintf(":%d", agentAPIPort))
				if err != nil {
					log.Printf("[agent] CRITICAL: cannot listen on new Nebula instance: %v", err)
					return
				}

				srv = newServer()
				go func() {
					if err := srv.Serve(newLn); err != nil && err != http.ErrServerClosed {
						log.Printf("[agent] Serve after Nebula restart: %v", err)
					}
				}()
				log.Printf("[agent] HTTP server restarted on new Nebula mesh listener")
			}
		}
	} else {
		// No Nebula config — listen on OS stack.
		log.Printf("[agent] no Nebula config at %s, running on OS stack", *nebulaConfig)
		startOSListener()
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("Shutting down agent...")
	renewCancel()
	serveMu.Lock()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	srv.Shutdown(shutCtx)
	shutCancel()
	serveMu.Unlock()

	// Clean up DNS configuration.
	cleanupDNS(activeDNSConfig)

	// Close Nebula.
	nebulaMu.Lock()
	if currentNebula != nil {
		currentNebula.Close()
		currentNebula = nil
	}
	nebulaMu.Unlock()
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
func warmPeersFromHeartbeat(endpoint string) {
	nodeID, _ := os.ReadFile(filepath.Join(configDir, "node-id"))
	token, _ := os.ReadFile(filepath.Join(configDir, "token"))
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
		Peers []string `json:"peers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || len(body.Peers) == 0 {
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
