package client

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/slackhq/nebula/cert"
)

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

	httpClient := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	resp, err := httpClient.Do(req)
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
// Tries kernel TUN first (if requested), falls back to userspace, returns nil
// if all fail. Backward-compat shim around startMeshWithError that discards
// the last error — keep using this from boot paths where we already log the
// underlying error and just need a yes/no.
func startMesh(configPath, tunMode string) meshService {
	svc, _ := startMeshWithError(configPath, tunMode)
	return svc
}

// startMeshWithError starts Nebula like startMesh but ALSO returns the
// last underlying error when both kernel-TUN and userspace fail. Callers
// that surface failure to the user (the connect retry loop) need this to
// distinguish port-bind failures ("address already in use") from cert /
// config / network failures, since the user-actionable message depends on
// which class of failure occurred. Pre-fix the connect retry loop reported
// "another hop-agent is using the network port" whenever startMesh returned
// nil regardless of the actual cause — most painfully on cert-clock-skew
// failures where the right action is "fix the clock", not "remove a
// conflicting install".
func startMeshWithError(configPath, tunMode string) (meshService, error) {
	if tunMode == "kernel" {
		if err := ensureWinTun(); err != nil {
			log.Printf("[agent] WARNING: wintun setup failed: %v", err)
		}
		svc, err := startNebulaKernelTun(configPath)
		if err != nil {
			log.Printf("[agent] WARNING: kernel TUN failed: %v (falling back to userspace)", err)
			// Fall through to userspace.
		} else {
			return svc, nil
		}
	}

	svc, err := startNebula(configPath)
	if err != nil {
		log.Printf("[agent] WARNING: Nebula userspace failed: %v (falling back to OS stack)", err)
		return nil, err
	}
	return svc, nil
}
