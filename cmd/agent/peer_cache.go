package main

// Per-enrollment peer-endpoint cache. Persists the list of peer
// VPN-IP → advertised UDP endpoints learned from the HTTPS heartbeat
// (server-side `peerEndpoints` field, originally sourced from the
// lighthouse + portmap advertise_addr) so that on agent restart we can
// inject them into Nebula's hostmap BEFORE the first handshake fires.
//
// Why: without this cache, a freshly-restarted agent has an empty
// hostmap. The first packet to a peer triggers a handshake plus a
// concurrent HostQuery to the lighthouse. On cellular, the relay path
// often wins the response race against direct NAT-punching, and the
// tunnel comes up on the relay leg. TCP starts streaming through the
// relay (high-latency, lossy), then Nebula reactively roams direct
// ~5-10 s later. By that time TCP's congestion window has collapsed
// and recovery on cellular takes 25+ s — the "first 30 s choppy"
// symptom.
//
// With cached endpoints injected at startup, the very first handshake
// can attempt direct, removing the race window entirely.
//
// On-disk format: small JSON, one file per enrollment, sibling of
// relay-state.json.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	peerCacheFile          = "peers.json"
	peerCacheSchemaVersion = 1
	// peerCacheTTL discards cached entries older than this on load.
	// Fresh enrollment / network re-IP could change peer addressing;
	// 24 h matches Nebula cert lifetime so the cache and cert age in
	// lockstep.
	peerCacheTTL = 24 * time.Hour
)

// peerCacheEntry is a single peer's advertised endpoints + last-seen
// wall-clock time (unix seconds).
type peerCacheEntry struct {
	Endpoints []string `json:"endpoints"`
	SeenAt    int64    `json:"seenAt"`
}

// peerCache is the on-disk shape. Indexed by peer VPN IP (string).
type peerCache struct {
	SchemaVersion int                       `json:"schemaVersion"`
	UpdatedAt     int64                     `json:"updatedAt"`
	Peers         map[string]peerCacheEntry `json:"peers"`
}

func peerCachePath(inst *meshInstance) string {
	return filepath.Join(inst.dir(), peerCacheFile)
}

// savePeerCache merges peerEndpoints into the existing on-disk cache
// (preserves entries for peers absent in this snapshot — they may
// re-appear on the next heartbeat) and writes atomically. Skips the
// write if the resulting cache is byte-identical to what's on disk.
func savePeerCache(inst *meshInstance, peerEndpoints map[string][]string) error {
	if inst == nil {
		return errors.New("nil meshInstance")
	}
	if len(peerEndpoints) == 0 {
		return nil
	}

	now := time.Now().Unix()
	cur, _ := loadPeerCacheRaw(inst)
	if cur == nil {
		cur = &peerCache{
			SchemaVersion: peerCacheSchemaVersion,
			Peers:         map[string]peerCacheEntry{},
		}
	}
	if cur.Peers == nil {
		cur.Peers = map[string]peerCacheEntry{}
	}

	changed := false
	for ipStr, eps := range peerEndpoints {
		eps = normalizeEndpoints(eps)
		if len(eps) == 0 {
			continue
		}
		// Validate VPN IP shape so we never persist garbage.
		if _, err := netip.ParseAddr(ipStr); err != nil {
			continue
		}
		prev, ok := cur.Peers[ipStr]
		if ok && stringSliceEqual(prev.Endpoints, eps) {
			// No endpoint change; bump SeenAt only.
			prev.SeenAt = now
			cur.Peers[ipStr] = prev
			changed = true
			continue
		}
		cur.Peers[ipStr] = peerCacheEntry{Endpoints: eps, SeenAt: now}
		changed = true
	}
	if !changed {
		return nil
	}

	cur.SchemaVersion = peerCacheSchemaVersion
	cur.UpdatedAt = now

	data, err := json.MarshalIndent(cur, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if existing, err := os.ReadFile(peerCachePath(inst)); err == nil {
		if string(existing) == string(data) {
			return nil
		}
	}
	return atomicWrite(peerCachePath(inst), data, 0644)
}

// loadPeerCacheRaw returns the on-disk cache as-is. (nil, nil) if no
// file exists. Caller is responsible for filtering by TTL.
func loadPeerCacheRaw(inst *meshInstance) (*peerCache, error) {
	if inst == nil {
		return nil, errors.New("nil meshInstance")
	}
	data, err := os.ReadFile(peerCachePath(inst))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var c peerCache
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", peerCachePath(inst), err)
	}
	if c.SchemaVersion != peerCacheSchemaVersion {
		// Unknown future schema — refuse to use it, but don't crash.
		return nil, fmt.Errorf("peer cache %s: unsupported schemaVersion %d", peerCachePath(inst), c.SchemaVersion)
	}
	return &c, nil
}

// loadPeerCache returns a TTL-filtered view: only entries seen within
// peerCacheTTL of now. (nil, nil) if no usable file exists.
func loadPeerCache(inst *meshInstance) (*peerCache, error) {
	c, err := loadPeerCacheRaw(inst)
	if err != nil || c == nil {
		return c, err
	}
	cutoff := time.Now().Unix() - int64(peerCacheTTL.Seconds())
	for ip, e := range c.Peers {
		if e.SeenAt < cutoff {
			delete(c.Peers, ip)
		}
	}
	if len(c.Peers) == 0 {
		return nil, nil
	}
	return c, nil
}

// injectCachedPeerEndpoints loads the cache and feeds each fresh-enough
// entry into Nebula's hostmap via patch 20's AddStaticHostMap. Returns
// the number of peers injected (0 if the cache is missing/empty/stale,
// or the Nebula control isn't available yet).
func injectCachedPeerEndpoints(inst *meshInstance) int {
	if inst == nil {
		return 0
	}
	c, err := loadPeerCache(inst)
	if err != nil || c == nil {
		return 0
	}
	ctrl := inst.control()
	if ctrl == nil {
		return 0
	}
	selfIP := inst.meshIP()
	subnet := inst.meshSubnet()
	n := 0
	for ipStr, e := range c.Peers {
		// Shared filter (Fix E + Layer 3) — drops self-loops and
		// cross-network entries before they reach the hostmap.
		vpn, ok := acceptPeerEndpoint(inst.name(), ipStr, selfIP, subnet)
		if !ok {
			continue
		}
		addrs := make([]netip.AddrPort, 0, len(e.Endpoints))
		for _, s := range e.Endpoints {
			ap, err := netip.ParseAddrPort(s)
			if err != nil || !ap.IsValid() {
				continue
			}
			addrs = append(addrs, ap)
		}
		if len(addrs) == 0 {
			continue
		}
		ctrl.ReplaceStaticHostMap(vpn, addrs)
		warmEndpointPath(addrs)
		n++
	}
	return n
}

// warmEndpointPath fires a single 1-byte UDP packet to each endpoint in
// parallel goroutines. Purpose: open the cellular CGNAT outbound flow
// AND populate the kernel's ARP/route cache toward the destination so
// Nebula's first real handshake (microseconds later) hits a warm path.
//
// Cross-platform — pure stdlib `net.Dial("udp", ...)`. Works on
// Darwin, Linux, Windows identically. On WiFi LAN it's effectively a
// free no-op (route cache populates regardless). On cellular CGNAT
// it's load-bearing: the first outbound packet from this host to the
// peer's public endpoint anchors a flow mapping that the inbound
// handshake response can reuse.
//
// Best-effort: any error (network unreachable, route failure, etc.)
// is silently ignored. We never block the caller.
func warmEndpointPath(addrs []netip.AddrPort) {
	for _, ap := range addrs {
		ap := ap
		go func() {
			d := net.Dialer{Timeout: 500 * time.Millisecond}
			conn, err := d.Dial("udp", ap.String())
			if err != nil {
				return
			}
			defer conn.Close()
			_ = conn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
			_, _ = conn.Write([]byte{0})
		}()
	}
}

// normalizeEndpoints sorts + dedups + drops invalid AddrPort strings
// so byte-equality of the cache reflects semantic equality (avoids
// rewriting the file on harmless map-iteration reorderings).
func normalizeEndpoints(eps []string) []string {
	if len(eps) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(eps))
	for _, s := range eps {
		if s == "" || seen[s] {
			continue
		}
		if _, err := netip.ParseAddrPort(s); err != nil {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
