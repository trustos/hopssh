package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/slackhq/nebula/cert"
	"github.com/trustos/hopssh/internal/buildinfo"
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
//
// One goroutine is started per meshInstance; each runs independently
// with its own heartbeat cadence, backoff state, and wake channel.
func runHeartbeat(ctx context.Context, inst *meshInstance) {
	const (
		normalInterval = 60 * time.Second
		initialRetry   = 5 * time.Second
		maxRetry       = 2 * time.Minute
	)

	// Send initial heartbeat immediately.
	failing := sendHeartbeat(inst) != nil
	retryInterval := initialRetry

	next := normalInterval
	if failing {
		next = initialRetry
	}

	timer := time.NewTimer(addJitter(next))
	defer timer.Stop()

	// fire sends one heartbeat and schedules the next tick based on
	// success/failure. Shared between the scheduled-timer path and the
	// wake-triggered out-of-cycle path so both obey the same
	// backoff/recovery state machine.
	fire := func() {
		err := sendHeartbeat(inst)
		if err != nil {
			if !failing {
				failing = true
				retryInterval = initialRetry
				log.Printf("[heartbeat %s] failed, switching to fast retry: %v", inst.name(), err)
			} else {
				retryInterval *= 2
				if retryInterval > maxRetry {
					retryInterval = maxRetry
				}
			}
			timer.Reset(addJitter(retryInterval))
		} else {
			if failing {
				log.Printf("[heartbeat %s] recovered after retry", inst.name())
				failing = false
				retryInterval = initialRetry
			}
			timer.Reset(addJitter(normalInterval))
		}
	}

	// drainTimer stops the scheduled timer and drains any pending tick,
	// so an out-of-cycle fire doesn't race with a scheduled one.
	drainTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			fire()
		case <-inst.heartbeatTrigger:
			log.Printf("[heartbeat %s] wake/network-change triggered out-of-cycle heartbeat", inst.name())
			drainTimer()
			fire()
		}
	}
}

func sendHeartbeat(inst *meshInstance) error {
	// Build the heartbeat body. Include peer counts + per-peer detail
	// when Nebula control is available (both kernel-TUN and userspace
	// modes expose it). Omit the peer fields when unavailable — the
	// server preserves the last known good values via COALESCE rather
	// than overwriting with zeros/empty.
	//
	// Multi-network-per-agent (roadmap #29): each instance fires its
	// own POSTs independently with its own nodeID + token. Body schema
	// stays singular; the control plane never sees cross-instance data.
	reqBody := map[string]any{
		"nodeId":       inst.nodeID(),
		"agentVersion": buildinfo.Version, // "vX.Y.Z" (tagged) or "vX.Y.Z-N-gSHORTSHA(-dirty)" (dev)
	}
	if direct, relayed, peers, ok := collectPeerState(inst.control()); ok {
		reqBody["peersDirect"] = direct
		reqBody["peersRelayed"] = relayed
		if len(peers) > 0 {
			reqBody["peers"] = peers
		}
	}
	reqBodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	authToken, err := readInstanceToken(inst)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", inst.endpoint()+"/api/heartbeat", bytes.NewReader(reqBodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		// With multi-enrollment we can no longer os.Exit on a single
		// instance's revocation without taking down the other
		// enrollments. Fail this POST loudly and let the outer retry
		// loop back off. Operators will see "offline" on the
		// dashboard and remove the node; the agent side's full
		// cleanup ships with `hop-agent leave` (Phase D).
		return fmt.Errorf("401: node deleted or token revoked for enrollment %q", inst.name())
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// Warm peer tunnels from heartbeat response. Also ingest peer-relay
	// info (Pillar 3) — `amRelay` flips this node into relay mode,
	// `relays` is the list of OTHER nodes the agent should add to its
	// `relay.relays` set so it can use them as fallback paths.
	//
	// `peerEndpoints` carries each peer's advertised UDP endpoints from
	// the server's lighthouse cache (includes NAT-PMP mappings). Injected
	// into Nebula's hostmap via patch 20's AddStaticHostMap so the agent
	// can handshake directly with peers even when the UDP lighthouse is
	// unreachable (e.g. carrier-filtered cellular to Oracle Cloud).
	var body struct {
		Peers         []string            `json:"peers"`
		Relays        []string            `json:"relays"`
		AmRelay       bool                `json:"amRelay"`
		PeerEndpoints map[string][]string `json:"peerEndpoints"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err == nil {
		if len(body.PeerEndpoints) > 0 {
			injectPeerEndpoints(inst, body.PeerEndpoints)
		}
		if len(body.Peers) > 0 {
			go warmPeers(body.Peers)
		}
		_ = saveRelayState(inst, body.AmRelay, body.Relays)
	}
	return nil
}

// readInstanceToken reads the bearer token from the instance's subdir.
// Cached token removal (re-enrollment) is rare; we re-read each time
// rather than caching so credential rotation picks up automatically.
func readInstanceToken(inst *meshInstance) (string, error) {
	data, err := os.ReadFile(filepath.Join(inst.dir(), "token"))
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// injectPeerEndpoints feeds peer advertised UDP endpoints (learned by the
// server's lighthouse, e.g. mini's NAT-PMP mapping `46.10.240.91:4242`)
// directly into this instance's Nebula hostmap. This is the agent-side
// counterpart to patch 20's AddStaticHostMap.
//
// Why: when UDP to the lighthouse is carrier-blocked (iPhone hotspot to
// Oracle Cloud, etc.), the agent can't learn peer endpoints via the
// normal Nebula HostQuery flow. The HTTPS control-plane heartbeat is
// still reachable, so the server pushes peer endpoints in-band via the
// `peerEndpoints` response field and we inject them here. MBP can then
// handshake directly to mini's home router public endpoint without
// needing a live lighthouse path.
//
// Best-effort: invalid entries are skipped; no lighthouse means no-op.
func injectPeerEndpoints(inst *meshInstance, peerEndpoints map[string][]string) {
	if inst == nil || len(peerEndpoints) == 0 {
		return
	}
	ctrl := inst.control()
	if ctrl != nil {
		for ipStr, epStrs := range peerEndpoints {
			vpn, err := netip.ParseAddr(ipStr)
			if err != nil || !vpn.IsValid() {
				continue
			}
			addrs := make([]netip.AddrPort, 0, len(epStrs))
			for _, s := range epStrs {
				ap, err := netip.ParseAddrPort(s)
				if err != nil || !ap.IsValid() {
					continue
				}
				addrs = append(addrs, ap)
			}
			if len(addrs) == 0 {
				continue
			}
			ctrl.AddStaticHostMap(vpn, addrs)
		}
	}
	// Persist the snapshot so the next agent restart can inject the
	// same endpoints BEFORE the first handshake fires (eliminates the
	// relay-vs-direct race that collapses cold-start TCP cwnd).
	if err := savePeerCache(inst, peerEndpoints); err != nil {
		log.Printf("[agent %s] peer cache save failed: %v", inst.name(), err)
	}
}

func warmPeers(peers []string) {
	for _, ip := range peers {
		d := net.Dialer{Timeout: time.Second}
		if conn, err := d.Dial("tcp", net.JoinHostPort(ip, "41820")); err == nil {
			conn.Close()
		}
	}
}

// runCertRenewal runs a background loop that renews the Nebula certificate
// before it expires. Renews at 50% lifetime (12h for a 24h cert).
// Exits the process if the node has been deleted (HTTP 401).
//
// One goroutine per meshInstance; each watches its own cert.
func runCertRenewal(ctx context.Context, inst *meshInstance) {
	for {
		renewAt, err := timeUntilRenewal(inst)
		if err != nil {
			log.Printf("[renew %s] failed to determine renewal time: %v (retrying in 5m)", inst.name(), err)
			renewAt = 5 * time.Minute
		}

		log.Printf("[renew %s] next renewal in %s", inst.name(), renewAt.Truncate(time.Second))

		select {
		case <-ctx.Done():
			return
		case <-time.After(renewAt):
		}

		if err := renewCert(inst); err != nil {
			log.Printf("[renew %s] renewal failed: %v", inst.name(), err)
			// Retry with backoff: 1m, 2m, 4m, ..., capped at 30m, max 12 attempts.
			backoff := time.Minute
			for attempt := 0; attempt < 12; attempt++ {
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				if err := renewCert(inst); err != nil {
					log.Printf("[renew %s] retry %d failed: %v", inst.name(), attempt+1, err)
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
func timeUntilRenewal(inst *meshInstance) (time.Duration, error) {
	certPEM, err := os.ReadFile(filepath.Join(inst.dir(), "node.crt"))
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
func renewCert(inst *meshInstance) error {
	reqBody := fmt.Sprintf(`{"nodeId":%q}`, inst.nodeID())
	req, err := http.NewRequest("POST", inst.endpoint()+"/api/renew", strings.NewReader(reqBody))
	if err != nil {
		return err
	}
	authToken, err := readInstanceToken(inst)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		// Multi-enrollment: see the matching note in sendHeartbeat.
		// One deleted node shouldn't take down the whole agent.
		return fmt.Errorf("401: node deleted or token revoked for enrollment %q", inst.name())
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
	certPath := filepath.Join(inst.dir(), "node.crt")
	keyPath := filepath.Join(inst.dir(), "node.key")

	if err := atomicWrite(certPath, []byte(renewResp.NodeCert), 0644); err != nil {
		return fmt.Errorf("write cert: %w", err)
	}
	if err := atomicWrite(keyPath, []byte(renewResp.NodeKey), 0600); err != nil {
		return fmt.Errorf("write key: %w", err)
	}

	// Apply server-pushed Nebula config if present.
	if renewResp.NebulaConfig != nil {
		if err := applyNebulaConfigUpdate(inst, renewResp.NebulaConfig); err != nil {
			log.Printf("[renew %s] failed to apply config update: %v (continuing with old config)", inst.name(), err)
		}
	}

	// Signal Nebula to reload certs (and pick up any config changes).
	reloadNebula(inst)

	log.Printf("[renew %s] certificate renewed successfully", inst.name())
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
func applyNebulaConfigUpdate(inst *meshInstance, update *nebulaConfigUpdate) error {
	configPath := filepath.Join(inst.dir(), "nebula.yaml")
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

	// Server-pushed MTU is intentionally IGNORED. The server stopped
	// pushing it as of v0.10.17 (see internal/api/renew.go for why), but
	// older servers in the wild may still send it. Honoring it would let
	// an older server clobber a newer agent's correct local MTU during
	// the renewal window — which is exactly the bug we're guarding
	// against. The agent's local `nebulacfg.TunMTU` (written by
	// `ensureP2PConfig` at startup) is the single source of truth for
	// MTU. If a per-network admin override is added later, it'll need a
	// distinct field name so we can honor it without re-introducing
	// this regression.
	_ = update.MTU

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
// - PKI paths match inst.dir() (fixes legacy flat-layout migrations
//   where the yaml still references the pre-migration paths)
func ensureP2PConfig(inst *meshInstance) {
	configPath := filepath.Join(inst.dir(), "nebula.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return
	}

	var cfg map[string]interface{}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return
	}

	changed := false

	// PKI paths: rewrite if they don't match the instance's subdir.
	// Covers both fresh enrollments (already correct) and post-
	// migration state where the yaml was moved but its pki block
	// still points at the flat-layout paths.
	pki := yamlMap(cfg, "pki")
	wantCA := filepath.Join(inst.dir(), "ca.crt")
	wantCert := filepath.Join(inst.dir(), "node.crt")
	wantKey := filepath.Join(inst.dir(), "node.key")
	if pki["ca"] != wantCA {
		pki["ca"] = wantCA
		changed = true
	}
	if pki["cert"] != wantCert {
		pki["cert"] = wantCert
		changed = true
	}
	if pki["key"] != wantKey {
		pki["key"] = wantKey
		changed = true
	}
	if changed {
		cfg["pki"] = pki
	}

	// Listen port is per-enrollment (assigned at enroll time, persisted
	// in Enrollment.ListenPort). Self-heal nebula.yaml if the on-disk
	// listen.port has drifted (legacy enrollments used port 0 = random;
	// the migrate step assigned a stable port and updated yaml, but a
	// later config write or hand-edit could re-introduce drift).
	listen := yamlMap(cfg, "listen")
	if inst.enrollment != nil && inst.enrollment.ListenPort > 0 {
		curPort, _ := listen["port"].(int)
		if curPort != inst.enrollment.ListenPort {
			listen["port"] = inst.enrollment.ListenPort
			changed = true
		}
	}
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

	// Faster handshake retry for quick tunnel establishment.
	handshakes := yamlMap(cfg, "handshakes")
	if ti, ok := handshakes["try_interval"]; !ok || ti != nebulacfg.HandshakeTryInterval {
		handshakes["try_interval"] = nebulacfg.HandshakeTryInterval
		cfg["handshakes"] = handshakes
		changed = true
	}

	// Normalize kernel-TUN dev name to hop-<enrollment>. Required on
	// Linux to avoid collisions when two instances run in the same
	// process (kernel rejects duplicate IFNAME). macOS ignores the
	// field (utun auto-assigned) but we still keep it consistent for
	// log readability.
	if tun, ok := cfg["tun"].(map[string]interface{}); ok {
		if mtu, hasMTU := tun["mtu"]; hasMTU {
			if mtuInt, ok := mtu.(int); ok && mtuInt != nebulacfg.TunMTU {
				tun["mtu"] = nebulacfg.TunMTU
				cfg["tun"] = tun
				changed = true
			}
		}
		// Only rewrite dev when the tun block actually has a dev key
		// (kernel mode) — userspace installs use `user: true` and no
		// dev field, which stays untouched.
		if _, hasDev := tun["dev"]; hasDev {
			want := meshIfaceName(inst.name())
			if tun["dev"] != want {
				tun["dev"] = want
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

	// Prefer local/private IPs for same-NAT peer discovery.
	lighthouse := yamlMap(cfg, "lighthouse")
	if _, ok := lighthouse["preferred_ranges"]; !ok {
		lighthouse["preferred_ranges"] = []string{
			"192.168.0.0/16",
			"172.16.0.0/12",
			"10.0.0.0/8",
		}
		cfg["lighthouse"] = lighthouse
		changed = true
	}

	// Detect physical interface and set local_allow_list.
	// This prevents Nebula from advertising overlay IPs (ZeroTier, etc.)
	// while still allowing the lighthouse to learn our public IP from
	// the UDP source address.
	host := extractHost(inst.endpoint())
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

	// Apply cached peer-relay state (Pillar 3): if the dashboard has
	// flagged this node as a relay, write `relay.am_relay: true`; if
	// other relay-capable peers exist, extend `relay.relays` with them.
	// `loadRelayState` returns nil if no cache exists yet (default
	// behavior — no relay role, only the lighthouse as relay).
	if state, _ := loadRelayState(inst); state != nil {
		relay := yamlMap(cfg, "relay")

		curAmRelay, _ := relay["am_relay"].(bool)
		if curAmRelay != state.AmRelay {
			relay["am_relay"] = state.AmRelay
			changed = true
		}

		// Merge cached peer-relay IPs into relay.relays without
		// dropping the lighthouse(s) the enrollment originally listed.
		if len(state.Relays) > 0 {
			merged := mergeRelayList(relay["relays"], state.Relays)
			if !relayListEqual(relay["relays"], merged) {
				relay["relays"] = merged
				changed = true
			}
		}

		cfg["relay"] = relay
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

// mergeRelayList combines the existing `relay.relays` list (whatever
// shape yaml.Unmarshal produced) with peer-relay IPs from cached
// state. Output is a sorted, deduped []interface{} compatible with
// yaml.Marshal — peer IPs are added IF NOT already present, original
// entries (the lighthouse) are preserved.
func mergeRelayList(existing any, peerRelays []string) []any {
	seen := map[string]bool{}
	out := []any{}
	switch list := existing.(type) {
	case []any:
		for _, item := range list {
			if s, ok := item.(string); ok && s != "" && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	case []string:
		for _, s := range list {
			if s != "" && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	for _, ip := range peerRelays {
		if ip == "" || seen[ip] {
			continue
		}
		seen[ip] = true
		out = append(out, ip)
	}
	return out
}

// relayListEqual compares two yaml-shaped relay lists for equality,
// treating string and any-string interchangeably.
func relayListEqual(a, b any) bool {
	as := relayListAsStrings(a)
	bs := relayListAsStrings(b)
	if len(as) != len(bs) {
		return false
	}
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

func relayListAsStrings(v any) []string {
	switch list := v.(type) {
	case []any:
		out := make([]string, 0, len(list))
		for _, x := range list {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return list
	}
	return nil
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
// Supports both userspace and kernel TUN modes. Scoped to one instance.
func reloadNebula(inst *meshInstance) {
	inst.svcMu.Lock()

	configPath := filepath.Join(inst.dir(), "nebula.yaml")
	tunMode := readTunMode(inst)

	if inst.svc == nil {
		// Nebula never started — e.g. cert was expired at boot. Try a fresh
		// start now that we have a renewed cert. This is the fix for the
		// "screen sharing broken after overnight sleep" bug: agent boots with
		// expired cert → Nebula fails → falls back to OS stack → renewal
		// gets fresh cert → this path starts Nebula for the first time.
		inst.svcMu.Unlock()
		log.Printf("[renew %s] no embedded Nebula instance — attempting cold start with renewed cert", inst.name())

		newSvc := startMesh(configPath, tunMode)
		if newSvc == nil {
			log.Printf("[renew %s] Nebula cold start failed even after cert renewal", inst.name())
			return
		}

		inst.setSvc(newSvc)

		log.Printf("[renew %s] Nebula started after cert renewal (mode: %s)", inst.name(), tunMode)

		// Configure DNS (kernel TUN mode only).
		if tunMode == "kernel" {
			inst.dnsConfig = readDNSConfig(inst)
			configureDNS(inst, inst.dnsConfig)
		}

		// Warm tunnels so Screen Sharing HP mode works immediately.
		warmTunnel(configPath)
		if endpoint := inst.endpoint(); endpoint != "" {
			warmPeersFromHeartbeat(inst, endpoint)
		}

		// (Re)start network-change watcher bound to the fresh ctrl.
		if ctrl := newSvc.NebulaControl(); ctrl != nil && inst.endpoint() != "" {
			inst.startWatcher(ctrl)
		}

		// Re-inject any live portmap mapping into the fresh lighthouse's
		// advertise_addrs (cold-start path: portmap wasn't running yet
		// so this is effectively a no-op, but kept for symmetry).
		inst.reinjectPortmapAddr()

		// Swap HTTP listener from OS stack to mesh.
		if inst.onRestart != nil {
			inst.onRestart(newSvc)
		}
		return
	}

	// Hot-restart path: tear down the old watcher first so it doesn't
	// outlive its Control, then close the old svc, start the new one.
	inst.stopWatcher()
	oldSvc := inst.svc
	inst.svc = nil
	inst.svcMu.Unlock()
	oldSvc.Close()

	var newSvc meshService
	var err error
	if tunMode == "kernel" {
		newSvc, err = startNebulaKernelTun(configPath)
	} else {
		newSvc, err = startNebula(configPath)
	}

	if err != nil {
		log.Printf("[renew %s] CRITICAL: failed to restart Nebula after cert renewal: %v", inst.name(), err)
		log.Printf("[renew %s] agent will lose mesh connectivity when old cert expires", inst.name())
		return
	}

	inst.setSvc(newSvc)

	// Spawn a fresh watcher against the new ctrl.
	if ctrl := newSvc.NebulaControl(); ctrl != nil && inst.endpoint() != "" {
		inst.startWatcher(ctrl)
	}

	// Re-inject any live portmap mapping into the new lighthouse's
	// advertise_addrs (the fresh Control starts with only config-file
	// addrs; without this, peers stop seeing our public endpoint until
	// the mapping next refreshes, which can be up to an hour).
	inst.reinjectPortmapAddr()

	log.Printf("[renew %s] Nebula restarted with new certificate (mode: %s)", inst.name(), tunMode)

	// Notify the HTTP server to recreate its mesh listener.
	if inst.onRestart != nil {
		inst.onRestart(newSvc)
	}
}

// readEndpointFromDisk reads the control plane endpoint URL from the
// persisted config (set during enrollment). Used by the cold-start path
// in reloadNebula() where agentEndpoint (local to runServe) isn't available.
func readEndpointFromDisk(inst *meshInstance) string {
	data, err := os.ReadFile(filepath.Join(inst.dir(), "endpoint"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
