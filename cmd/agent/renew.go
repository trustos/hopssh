package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/slackhq/nebula/cert"
	"github.com/trustos/hopssh/internal/nebulacfg"
	"gopkg.in/yaml.v3"
)

// runHeartbeat sends periodic heartbeats to the control plane so the
// dashboard shows the node as "online".
//
// Normal mode: every 5 minutes. On failure: fast retry with exponential
// backoff (5s → 10s → 20s → ... → 2m cap) so agents recover within seconds
// of a server redeploy. All intervals include ±10% jitter to prevent
// thundering herd across agents.
func runHeartbeat(ctx context.Context, endpoint, nodeID, agentToken string) {
	const (
		normalInterval = 5 * time.Minute
		initialRetry   = 5 * time.Second
		maxRetry       = 2 * time.Minute
	)

	// Send initial heartbeat immediately.
	failing := sendHeartbeat(endpoint, nodeID, agentToken) != nil
	retryInterval := initialRetry

	next := normalInterval
	if failing {
		next = initialRetry
	}

	timer := time.NewTimer(addJitter(next))
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			err := sendHeartbeat(endpoint, nodeID, agentToken)

			if err != nil {
				if !failing {
					failing = true
					retryInterval = initialRetry
					log.Printf("[heartbeat] failed, switching to fast retry: %v", err)
				} else {
					retryInterval *= 2
					if retryInterval > maxRetry {
						retryInterval = maxRetry
					}
				}
				timer.Reset(addJitter(retryInterval))
			} else {
				if failing {
					log.Printf("[heartbeat] recovered after retry")
					failing = false
					retryInterval = initialRetry
				}
				timer.Reset(addJitter(normalInterval))
			}
		}
	}
}

func sendHeartbeat(endpoint, nodeID, agentToken string) error {
	reqBody := fmt.Sprintf(`{"nodeId":%q}`, nodeID)
	req, err := http.NewRequest("POST", endpoint+"/api/heartbeat", strings.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+agentToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		log.Fatal("[heartbeat] node has been deleted or token revoked — shutting down")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// runCertRenewal runs a background loop that renews the Nebula certificate
// before it expires. Renews at 50% lifetime (12h for a 24h cert).
// Exits the process if the node has been deleted (HTTP 401).
func runCertRenewal(ctx context.Context, endpoint, nodeID, agentToken string) {
	for {
		renewAt, err := timeUntilRenewal()
		if err != nil {
			log.Printf("[renew] failed to determine renewal time: %v (retrying in 5m)", err)
			renewAt = 5 * time.Minute
		}

		log.Printf("[renew] next renewal in %s", renewAt.Truncate(time.Second))

		select {
		case <-ctx.Done():
			return
		case <-time.After(renewAt):
		}

		if err := renewCert(endpoint, nodeID, agentToken); err != nil {
			log.Printf("[renew] renewal failed: %v", err)
			// Retry with backoff: 1m, 2m, 4m, ..., capped at 30m, max 12 attempts.
			backoff := time.Minute
			for attempt := 0; attempt < 12; attempt++ {
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				if err := renewCert(endpoint, nodeID, agentToken); err != nil {
					log.Printf("[renew] retry %d failed: %v", attempt+1, err)
					backoff *= 2
					if backoff > 30*time.Minute {
						backoff = 30 * time.Minute
					}
					continue
				}
				break // success
			}
		}
	}
}

// timeUntilRenewal reads the current cert and returns the duration until
// renewal should happen (50% of remaining validity).
func timeUntilRenewal() (time.Duration, error) {
	certPEM, err := os.ReadFile(filepath.Join(configDir, "node.crt"))
	if err != nil {
		return 0, fmt.Errorf("read cert: %w", err)
	}

	c, _, err := cert.UnmarshalCertificateFromPEM(certPEM)
	if err != nil {
		return 0, fmt.Errorf("parse cert: %w", err)
	}

	notAfter := c.NotAfter()
	remaining := time.Until(notAfter)
	if remaining <= 0 {
		return 0, nil // already expired, renew immediately
	}

	// Renew at 50% of remaining lifetime, with ±10% jitter to spread load.
	base := remaining / 2
	jitter := time.Duration(rand.Int63n(int64(remaining)/5)) - (remaining / 10)
	return base + jitter, nil
}

// renewCert calls the control plane's /api/renew endpoint to get a fresh cert.
func renewCert(endpoint, nodeID, agentToken string) error {
	reqBody := fmt.Sprintf(`{"nodeId":%q}`, nodeID)
	req, err := http.NewRequest("POST", endpoint+"/api/renew", strings.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+agentToken)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		log.Fatal("[renew] node has been deleted or token revoked — shutting down")
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}

	var renewResp struct {
		NodeCert     string              `json:"nodeCert"`
		NodeKey      string              `json:"nodeKey"`
		NebulaConfig *nebulaConfigUpdate `json:"nebulaConfig,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&renewResp); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	if renewResp.NodeCert == "" || renewResp.NodeKey == "" {
		return fmt.Errorf("empty cert or key in response")
	}

	// Write new cert atomically (temp file + rename) to prevent partial reads.
	certPath := filepath.Join(configDir, "node.crt")
	keyPath := filepath.Join(configDir, "node.key")

	if err := atomicWrite(certPath, []byte(renewResp.NodeCert), 0644); err != nil {
		return fmt.Errorf("write cert: %w", err)
	}
	if err := atomicWrite(keyPath, []byte(renewResp.NodeKey), 0600); err != nil {
		return fmt.Errorf("write key: %w", err)
	}

	// Apply server-pushed Nebula config if present.
	if renewResp.NebulaConfig != nil {
		if err := applyNebulaConfigUpdate(renewResp.NebulaConfig); err != nil {
			log.Printf("[renew] failed to apply config update: %v (continuing with old config)", err)
		}
	}

	// Signal Nebula to reload certs (and pick up any config changes).
	reloadNebula()

	log.Printf("[renew] certificate renewed successfully")
	return nil
}

// nebulaConfigUpdate contains server-pushed Nebula settings.
// Pointer fields: nil means "don't change", non-nil means "set to this value".
type nebulaConfigUpdate struct {
	UseRelays  *bool  `json:"useRelays,omitempty"`
	PunchBack  *bool  `json:"punchBack,omitempty"`
	PunchDelay string `json:"punchDelay,omitempty"`
	MTU        *int   `json:"mtu,omitempty"`
	ListenPort *int   `json:"listenPort,omitempty"`
}

// applyNebulaConfigUpdate merges server-pushed settings into the local nebula.yaml.
func applyNebulaConfigUpdate(update *nebulaConfigUpdate) error {
	configPath := filepath.Join(configDir, "nebula.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	out, changed, err := mergeNebulaConfig(data, update)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}

	if err := atomicWrite(configPath, out, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	log.Printf("[renew] nebula config updated from server")
	return nil
}

// mergeNebulaConfig applies a config update to raw YAML bytes and returns the
// result. Pure function — no side effects, easy to test. Returns (output, changed, error).
func mergeNebulaConfig(data []byte, update *nebulaConfigUpdate) ([]byte, bool, error) {
	var cfg map[string]interface{}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, false, fmt.Errorf("parse config: %w", err)
	}

	changed := false

	if update.UseRelays != nil {
		relay := yamlMap(cfg, "relay")
		relay["use_relays"] = *update.UseRelays
		cfg["relay"] = relay
		changed = true
	}

	if update.PunchBack != nil || update.PunchDelay != "" {
		punchy := yamlMap(cfg, "punchy")
		if update.PunchBack != nil {
			punchy["punch_back"] = *update.PunchBack
			changed = true
		}
		if update.PunchDelay != "" {
			punchy["delay"] = update.PunchDelay
			changed = true
		}
		cfg["punchy"] = punchy
	}

	// Update listen.port — fixed port is critical for NAT hole punching.
	if update.ListenPort != nil {
		listen := yamlMap(cfg, "listen")
		listen["port"] = *update.ListenPort
		cfg["listen"] = listen
		changed = true
	}

	// Update tun.mtu only if tun section already has mtu (kernel TUN mode).
	if update.MTU != nil {
		if tun, ok := cfg["tun"].(map[string]interface{}); ok {
			if _, hasMTU := tun["mtu"]; hasMTU {
				tun["mtu"] = *update.MTU
				cfg["tun"] = tun
				changed = true
			}
		}
	}

	if !changed {
		return data, false, nil
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, false, fmt.Errorf("marshal config: %w", err)
	}
	return out, true, nil
}

// yamlMap returns a nested map from a config, creating it if absent.
func yamlMap(cfg map[string]interface{}, key string) map[string]interface{} {
	if m, ok := cfg[key].(map[string]interface{}); ok {
		return m
	}
	m := make(map[string]interface{})
	cfg[key] = m
	return m
}

// ensureP2PConfig updates nebula.yaml with settings critical for P2P:
// - Fixed listen port (NAT mapping stability)
// - target_all_remotes (continuous relay→direct upgrade)
// - local_allow_list with physical interface (prevents overlay-within-overlay)
// - Fast punch timing
func ensureP2PConfig(endpoint string) {
	configPath := filepath.Join(configDir, "nebula.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return
	}

	var cfg map[string]interface{}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return
	}

	changed := false

	// Fixed listen port for stable NAT mappings.
	listen := yamlMap(cfg, "listen")
	if port, ok := listen["port"]; !ok || port != nebulacfg.ListenPort {
		listen["port"] = nebulacfg.ListenPort
		changed = true
	}
	// Remove oversized socket buffers that cause bufferbloat.
	if _, ok := listen["read_buffer"]; ok {
		delete(listen, "read_buffer")
		changed = true
	}
	if _, ok := listen["write_buffer"]; ok {
		delete(listen, "write_buffer")
		changed = true
	}
	cfg["listen"] = listen

	// Ensure cipher matches default (AES-GCM, hardware-accelerated on Apple Silicon).
	if c, ok := cfg["cipher"]; !ok || c != nebulacfg.Cipher {
		cfg["cipher"] = nebulacfg.Cipher
		changed = true
	}

	// Punchy settings for fast P2P establishment.
	punchy := yamlMap(cfg, "punchy")
	if tar, ok := punchy["target_all_remotes"]; !ok || tar != true {
		punchy["target_all_remotes"] = true
		changed = true
	}
	if punchy["delay"] != nebulacfg.PunchDelay {
		punchy["delay"] = nebulacfg.PunchDelay
		changed = true
	}
	if punchy["respond_delay"] != nebulacfg.RespondDelay {
		punchy["respond_delay"] = nebulacfg.RespondDelay
		changed = true
	}
	if changed {
		cfg["punchy"] = punchy
	}

	// Update MTU if it's lower than the optimal value.
	if tun, ok := cfg["tun"].(map[string]interface{}); ok {
		if mtu, hasMTU := tun["mtu"]; hasMTU {
			if mtuInt, ok := mtu.(int); ok && mtuInt < nebulacfg.TunMTU {
				tun["mtu"] = nebulacfg.TunMTU
				cfg["tun"] = tun
				changed = true
			}
		}
	}

	// Parallel packet processing routines (effective on Linux with multiqueue TUN).
	if r, ok := cfg["routines"]; !ok || r != nebulacfg.Routines {
		cfg["routines"] = nebulacfg.Routines
		changed = true
	}

	// Detect physical interface and set local_allow_list.
	// This prevents Nebula from advertising overlay IPs (ZeroTier, etc.)
	// while still allowing the lighthouse to learn our public IP from
	// the UDP source address.
	host := extractHost(endpoint)
	if host != "" {
		if iface, err := nebulacfg.DetectPhysicalInterface(host); err == nil {
			lighthouse := yamlMap(cfg, "lighthouse")
			escaped := regexp.QuoteMeta(iface)
			lighthouse["local_allow_list"] = map[string]interface{}{
				"interfaces": map[string]interface{}{
					escaped: true,
				},
			}
			cfg["lighthouse"] = lighthouse
			changed = true
			log.Printf("[agent] local_allow_list set to interface %s", iface)
		} else {
			log.Printf("[agent] could not detect physical interface: %v", err)
		}
	}

	if !changed {
		return
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return
	}

	if err := atomicWrite(configPath, out, 0644); err != nil {
		log.Printf("[agent] WARNING: failed to update P2P config: %v", err)
		return
	}
	log.Printf("[agent] P2P config updated (port: %d, target_all_remotes: true)", nebulacfg.ListenPort)
}

// addJitter applies ±10% random jitter to a duration to spread agent load.
func addJitter(d time.Duration) time.Duration {
	jitter := time.Duration(float64(d) * 0.1 * (2*rand.Float64() - 1))
	return d + jitter
}

// atomicWrite writes data to a temp file then renames it to the target path.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// reloadNebula restarts the embedded Nebula instance to pick up new certs.
// Supports both userspace and kernel TUN modes.
func reloadNebula() {
	nebulaMu.Lock()

	if currentNebula == nil {
		nebulaMu.Unlock()
		log.Printf("[renew] no embedded Nebula instance to reload")
		return
	}

	configPath := filepath.Join(configDir, "nebula.yaml")
	tunMode := readTunMode()

	currentNebula.Close()

	var newSvc meshService
	var err error
	if tunMode == "kernel" {
		newSvc, err = startNebulaKernelTun(configPath)
	} else {
		newSvc, err = startNebula(configPath)
	}

	if err != nil {
		log.Printf("[renew] CRITICAL: failed to restart Nebula after cert renewal: %v", err)
		log.Printf("[renew] agent will lose mesh connectivity when old cert expires")
		currentNebula = nil
		nebulaMu.Unlock()
		return
	}

	currentNebula = newSvc
	nebulaMu.Unlock()

	log.Printf("[renew] Nebula restarted with new certificate (mode: %s)", tunMode)

	// Notify the HTTP server to recreate its mesh listener.
	// Called AFTER releasing nebulaMu to avoid deadlock.
	if onNebulaRestart != nil {
		onNebulaRestart(newSvc)
	}
}
