package main

// Phase G — agent reports its OWN observed UDP endpoints in the heartbeat
// POST so the control plane can distribute them to peers via HTTPS,
// without depending on UDP-to-lighthouse advertise_addrs propagation.
//
// Why: when an agent's UDP path to the lighthouse is filtered (e.g.
// iPhone Personal Hotspot blocks UDP to specific Oracle Cloud IPs),
// the lighthouse never learns the agent's current advertise_addrs,
// so the server's `peerEndpoints` response to OTHER peers becomes
// stale within minutes of carrier-CGNAT-flow expiry. The peer can
// still DIAL this agent (via cached endpoints from A1) but this
// agent's own endpoint info disappears from the system, so when its
// CGNAT mapping changes, no peer can find the new mapping.
//
// This closes the loop: HTTPS heartbeat carries the local-side view
// of where this agent is reachable. Server caches it per-node and
// merges into peer-endpoint responses.
//
// Cross-platform: pure stdlib net.Interfaces() — works identically
// on Darwin/Linux/Windows.

import (
	"net"
	"net/netip"
)

// selfEndpoints returns the list of `IP:port` strings that THIS agent
// believes it can be reached at, ordered by likely usefulness:
//   1. Public mapping from NAT-PMP/UPnP/PCP (most likely to work
//      from cellular peers behind CGNAT).
//   2. Non-loopback, non-link-local interface addresses paired with
//      the instance's listen port (useful for same-LAN peers and
//      same-NAT same-router peers where preferred_ranges hairpins).
//
// All entries are validated as netip.AddrPort before joining the list
// so the server never has to re-parse questionable strings. Returns
// nil if no listen port is known (cold-start race, very rare).
func selfEndpoints(inst *meshInstance) []string {
	if inst == nil {
		return nil
	}
	port := inst.enrollment.ListenPort
	if port == 0 {
		// Pre-A2-migration enrollments may not have a persisted port.
		// Skip silently rather than guess wrong.
		return nil
	}
	out := make([]string, 0, 4)
	seen := map[string]struct{}{}

	// Public mapping (NAT-PMP / UPnP / PCP). Highest priority — this
	// is the endpoint that actually works from the public Internet.
	if inst.portmap != nil {
		if pub := inst.portmap.Current(); pub.IsValid() {
			s := pub.String()
			if _, dup := seen[s]; !dup {
				seen[s] = struct{}{}
				out = append(out, s)
			}
		}
	}

	// Local interface addresses. Best-effort: any error in walking
	// interfaces drops the local part silently — the public mapping
	// alone is enough for cellular peers.
	addrs := localUnicastAddrs()
	for _, ip := range addrs {
		ap := netip.AddrPortFrom(ip, uint16(port))
		if !ap.IsValid() {
			continue
		}
		s := ap.String()
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// localUnicastAddrs returns this host's globally-routable-ish IPv4
// addresses across all up interfaces, excluding loopback, link-local,
// and the agent's own mesh IP (10.42.x.x — those are mesh-internal
// and useless to peers as a reachability hint).
//
// We intentionally include RFC1918 private addresses (192.168.x.x,
// 10.x.x.x outside our mesh range, 172.16-31.x.x): same-LAN peers
// can hit them directly, and the peer's `preferred_ranges` config
// will pick them over public addresses for hairpin-NAT-bypass
// scenarios.
func localUnicastAddrs() []netip.Addr {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	out := make([]netip.Addr, 0, 4)
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		ifAddrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range ifAddrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP.To4()
			if ip == nil {
				continue // IPv4 only for v1
			}
			addr, ok := netip.AddrFromSlice(ip)
			if !ok || !addr.IsValid() {
				continue
			}
			if addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsMulticast() || addr.IsUnspecified() {
				continue
			}
			// Skip the mesh subnet (10.42.0.0/16). Telling a peer "I'm
			// reachable at 10.42.1.7" via heartbeat is meaningless;
			// they already know that from the cert's vpn IP.
			if addr.Is4() {
				b := addr.As4()
				if b[0] == 10 && b[1] == 42 {
					continue
				}
			}
			out = append(out, addr)
		}
	}
	return out
}
