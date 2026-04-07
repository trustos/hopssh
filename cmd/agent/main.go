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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
	"github.com/trustos/hopssh/internal/buildinfo"
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
	authed := authMiddleware(authToken, mux)

	// Start cert renewal + heartbeat if endpoint + nodeID are available.
	renewCtx, renewCancel := context.WithCancel(context.Background())
	defer renewCancel()
	if epData, err := os.ReadFile(*endpointFile); err == nil {
		if idData, err := os.ReadFile(*nodeIDFile); err == nil {
			ep := strings.TrimSpace(string(epData))
			nodeID := strings.TrimSpace(string(idData))
			if ep != "" && nodeID != "" {
				go runCertRenewal(renewCtx, ep, nodeID, authToken)
				go runHeartbeat(renewCtx, ep, nodeID, authToken)
				log.Printf("[agent] cert auto-renewal + heartbeat enabled (endpoint: %s)", ep)
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
		// Normal path: listen on the Nebula mesh's userspace network stack.
		svc, err := startNebula(*nebulaConfig)
		if err != nil {
			log.Printf("[agent] WARNING: Nebula failed to start: %v (falling back to OS stack)", err)
			ln, err := net.Listen("tcp", fmt.Sprintf(":%d", agentAPIPort))
			if err != nil {
				log.Fatalf("Listen :%d: %v", agentAPIPort, err)
			}
			log.Printf("hop-agent listening on %s (OS stack fallback)", ln.Addr())
			go func() {
				if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
					log.Fatalf("Serve: %v", err)
				}
			}()
		} else {
			nebulaMu.Lock()
			currentNebula = svc
			nebulaMu.Unlock()
			log.Printf("[agent] Nebula mesh connected")

			meshLn, err := svc.Listen("tcp", fmt.Sprintf(":%d", agentAPIPort))
			if err != nil {
				log.Fatalf("Nebula mesh listen: %v", err)
			}
			log.Printf("hop-agent listening on :%d (Nebula mesh)", agentAPIPort)

			go func() {
				if err := srv.Serve(meshLn); err != nil && err != http.ErrServerClosed {
					log.Fatalf("Serve: %v", err)
				}
			}()

			// Register callback for Nebula restarts (cert renewal).
			// Called AFTER nebulaMu is released by reloadNebula().
			onNebulaRestart = func(newSvc *nebulaService) {
				serveMu.Lock()
				defer serveMu.Unlock()

				// Gracefully shut down current server.
				shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
				srv.Shutdown(shutCtx)
				shutCancel()

				// Create new listener on the new Nebula service.
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
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", agentAPIPort))
		if err != nil {
			log.Fatalf("Listen :%d: %v", agentAPIPort, err)
		}
		log.Printf("hop-agent listening on %s", ln.Addr())
		go func() {
			if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
				log.Fatalf("Serve: %v", err)
			}
		}()
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

	// Close Nebula.
	nebulaMu.Lock()
	if currentNebula != nil {
		currentNebula.Close()
		currentNebula = nil
	}
	nebulaMu.Unlock()
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

func handleShell(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[shell] WebSocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	// Determine shell: $SHELL env, then platform default, then fallbacks.
	shell := os.Getenv("SHELL")
	if shell == "" {
		// macOS default is zsh since Catalina; Linux typically has bash.
		if runtime.GOOS == "darwin" {
			shell = "/bin/zsh"
		} else {
			shell = "/bin/bash"
		}
	}
	if _, err := os.Stat(shell); err != nil {
		shell = "/bin/sh"
	}

	cmd := exec.Command(shell, "-l")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")

	ptmx, err := pty.Start(cmd)
	if err != nil {
		log.Printf("[shell] PTY start failed: %v", err)
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}
	defer func() {
		ptmx.Close()
		cmd.Process.Kill()
		// Wait with timeout to avoid blocking on zombie processes.
		waitDone := make(chan struct{})
		go func() {
			cmd.Wait()
			close(waitDone)
		}()
		select {
		case <-waitDone:
		case <-time.After(5 * time.Second):
			log.Printf("[shell] process %d did not exit after kill, abandoning", cmd.Process.Pid)
		}
	}()

	_ = pty.Setsize(ptmx, &pty.Winsize{Rows: 24, Cols: 80})

	done := make(chan struct{})

	// PTY stdout -> WebSocket
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if err != nil {
				return
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
				return
			}
		}
	}()

	// WebSocket -> PTY stdin
	go func() {
		for {
			msgType, msg, err := conn.ReadMessage()
			if err != nil {
				ptmx.Close()
				return
			}

			if msgType == websocket.BinaryMessage && len(msg) > 0 && msg[0] == shellResizePrefix {
				if len(msg) >= 5 {
					rows := uint16(msg[1])<<8 | uint16(msg[2])
					cols := uint16(msg[3])<<8 | uint16(msg[4])
					_ = pty.Setsize(ptmx, &pty.Winsize{Rows: rows, Cols: cols})
				}
				continue
			}

			ptmx.Write(msg)
		}
	}()

	<-done
}
