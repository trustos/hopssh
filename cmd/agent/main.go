package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
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
	"github.com/trustos/hopssh/internal/buildinfo"
	"github.com/trustos/hopssh/internal/client"

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
			client.RunHelp()
			return
		case "version", "--version":
			fmt.Printf("hop-agent %s (%s)\n", buildinfo.Version, buildinfo.Commit)
			return
		case "status":
			client.RunStatus(os.Args[2:])
			return
		case "info":
			client.RunInfo(os.Args[2:])
			return
		case "enroll":
			client.RunEnroll(os.Args[2:])
			return
		case "serve":
			runServe(os.Args[2:])
			return
		case "install":
			client.RunAgentInstall(os.Args[2:])
			return
		case "uninstall":
			client.RunAgentUninstall(os.Args[2:])
			return
		case "update":
			client.RunAgentUpdate(os.Args[2:])
			return
		case "restart":
			client.RunRestart(os.Args[2:])
			return
		case "stop":
			client.RunStop()
			return
		case "leave":
			client.RunLeave(os.Args[2:])
			return
		case "client":
			client.RunClientJoin(os.Args[2:])
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
	// handler that bridges Stop/Shutdown into shutdownCancel.
	_ = client.SvcIntegrateIfNeeded(shutdownCancel)

	client.CleanupOldBinary()
	client.StartPprofIfRequested()

	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgDir := fs.String("config-dir", "", "Override config directory")
	tokenFile := fs.String("token-file", "", "Path to the bearer token file")
	tokenFlag := fs.String("token", "", "Bearer token (overrides -token-file)")
	listenAddr := fs.String("listen", "", "Override listen address (bypasses mesh, uses OS stack)")
	mirrorDir := fs.String("mirror-dir", "", "When set, mirror the local-api token + port to <mirror-dir>/system-local-api-{token,port} for the desktop .app to discover the system agent (macOS only; written by `hop-agent install --migrate-from`)")
	fs.Parse(args)

	client.SetSystemMirrorDirOverride(strings.TrimSpace(*mirrorDir))

	// Resolve the effective config dir BEFORE the legacy migration
	// (which works on whatever path we hand it).
	effCfgDir := client.ResolveConfigDir(*cfgDir)

	// Migrate any pre-v0.10 flat layout into the new subdir layout.
	// Idempotent + safe on fresh installs.
	if err := client.MigrateLegacyLayout(effCfgDir); err != nil {
		log.Fatalf("Legacy config migration failed: %v", err)
	}

	// Build the per-instance HTTP mux. Stateless except for the auth
	// token which is layered in by the agentHTTPHook.
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

	c, err := client.NewClient(client.Config{
		ConfigDir: effCfgDir,
		UserAgent: "hopssh-agent/" + buildinfo.Version,
	})
	if err != nil {
		log.Fatalf("Client init: %v", err)
	}
	c.SetInstanceHTTPHook(&agentHTTPHook{mux: mux})
	defer c.Stop()

	// Phase Z (v0.10.91): periodic re-chown of mirror files if they're
	// still root-owned. No-op when the mirror dir override is empty.
	client.RunMirrorChownSelfHeal(shutdownCtx)

	// Always Start() the client BEFORE StartLocalAPI so c.runCtx is set
	// even on fresh installs with zero enrollments. With an empty list
	// Start() is a no-op aside from setting runCtx + running
	// migrateListenPorts on an empty registry. Without this, a device-flow
	// enrollment landing through the local API would call the auto-connect
	// path with c.runCtx == nil → panic in startInstance's
	// context.WithCancel (the v0.11.18 Linux fresh-install crash).
	if err := c.Start(shutdownCtx); err != nil {
		log.Fatalf("[agent] start: %v", err)
	}

	// Loopback HTTP API used by the desktop GUI shell (Tauri).
	if err := client.StartLocalAPI(shutdownCtx, c); err != nil {
		log.Printf("[agent] WARNING: local API not started: %v", err)
	}

	names := c.EnrollmentNames()

	if *listenAddr != "" && len(names) == 0 {
		// --listen overrides + no enrollment → OS-stack-only debug mode.
		if err := startDebugOSListener(c, mux, *tokenFlag, *tokenFile, *listenAddr); err != nil {
			log.Fatalf("%v", err)
		}
	} else if len(names) == 0 {
		// No enrollment + no --listen → soft-fail OS-stack listener so a
		// freshly-installed agent stays alive long enough for enrollment
		// via the local API.
		log.Printf("[agent] no enrollments found, awaiting enrollment (use the desktop client or 'hop-agent enroll')")
		if err := startDebugOSListener(c, mux, *tokenFlag, *tokenFile, fmt.Sprintf(":%d", client.AgentAPIPort)); err != nil {
			log.Printf("[agent] mesh API listener not started (this is expected for a fresh install): %v", err)
		}
	}
	// else: the with-enrollments case is already handled by c.Start() above.

	// Wait for Unix signals (SIGINT/SIGTERM) OR Windows SCM
	// Stop/Shutdown.
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
	_ = c.Stop()
}

// agentHTTPHook implements client.InstanceHTTPHook by wrapping the agent's
// stateless mux with bearer-token auth per instance. Mobile clients pass
// nil for the hook; this lives in cmd/agent only.
type agentHTTPHook struct{ mux http.Handler }

func (h *agentHTTPHook) BuildHandler(name, authToken string) http.Handler {
	return authMiddleware(authToken, h.mux)
}

// startDebugOSListener serves the mux directly on the OS stack using a
// bearer token from --token or --token-file. Used only when no enrollment
// exists — for ad-hoc local testing.
func startDebugOSListener(c *client.Client, mux http.Handler, tokenFlag, tokenFilePath, listenAddr string) error {
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
	return c.RegisterDebugListener(authMiddleware(authToken, mux), listenAddr)
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
