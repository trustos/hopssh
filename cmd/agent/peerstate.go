package main

import (
	"github.com/slackhq/nebula"
)

// PeerDetail is one entry in the per-peer list carried in the agent's
// heartbeat body. The server stores this verbatim on nodes.peer_state
// (JSON blob). The dashboard reads it to drive the per-peer drill-down
// and the topology diagram.
type PeerDetail struct {
	VpnAddr          string `json:"vpnAddr"`                    // peer's mesh IP (e.g. "10.42.1.7")
	Direct           bool   `json:"direct"`                     // true = P2P UDP; false = relay-routed
	LastHandshakeSec int64  `json:"lastHandshakeSec,omitempty"` // 0 = unknown (Nebula's public API doesn't expose it today)
	RemoteAddr       string `json:"remoteAddr,omitempty"`       // observed remote UDP endpoint when Direct
	// RTTms is the EWMA-smoothed TCP-connect round-trip in ms,
	// measured by runPathQuality probing the peer's mesh listener
	// (:41820). 0 means "no sample yet" (e.g. brand-new peer, or
	// peer is relay-routed so we don't probe). Smoothed via EWMA
	// alpha=0.3 across 10s probe intervals so the value tracks
	// real path quality without being yanked by single outliers.
	RTTms int `json:"rttMs,omitempty"`
}

// maxPeersPerReport caps the reported per-peer slice to prevent
// pathological payload growth on large meshes. 100 covers any realistic
// fleet; beyond that the aggregate counts still tell the story.
const maxPeersPerReport = 100

// collectPeerState queries Nebula's hostmap and summarizes the agent's
// current peers, ONE entry per unique VPN address even when Nebula's
// hostmap holds multiple HostInfo records for the same peer (which
// happens during the transition from relay to direct: the old relay-
// routed session lingers in the hostmap while the new direct session
// is established, and Nebula's connection_manager prunes the stale
// one ~90 s later).
//
// Classification per peer (after merging all HostInfos that share a
// VpnAddr):
//
//   - direct:  ANY HostInfo for that peer has a valid CurrentRemote
//     (UDP direct / hole-punched P2P). Direct wins over relay because
//     Nebula prefers the direct path for actual data — the relay
//     entry in this case is a soon-to-be-pruned ghost, not the
//     active path.
//
//   - relayed: NO HostInfo has a valid CurrentRemote, but at least
//     one has CurrentRelaysToMe non-empty (the lighthouse-as-relay
//     path used when hole punching fails: symmetric NAT, CGNAT,
//     restrictive firewalls).
//
//   - peers:   per-peer detail, one entry per classified peer.
//     Capped at maxPeersPerReport.
//
// Peers with neither a valid CurrentRemote nor any relay across all
// HostInfo records are skipped — pure ghosts.
//
// ok is false when ctrl is nil (mesh not started, or agent starting
// up) — callers omit peer fields from the heartbeat in that case so
// the server's COALESCE preserves the last known good value rather
// than overwriting with zeros/empty.
func collectPeerState(ctrl *nebula.Control, pq *pathQuality) (direct, relayed int, peers []PeerDetail, ok bool) {
	if ctrl == nil {
		return 0, 0, nil, false
	}
	hosts := ctrl.ListHostmapHosts(false)

	// Merge all HostInfos that share a VpnAddr, preferring the entry
	// with a valid CurrentRemote. Iteration order from ListHostmapHosts
	// is undefined (map range), so we MUST process all HostInfos for a
	// peer before classifying — can't short-circuit on first hit.
	type merged struct {
		direct     bool           // true if any HostInfo has a valid CurrentRemote
		hasRelay   bool           // true if any has CurrentRelaysToMe (informational)
		remoteAddr string         // populated when direct (first valid CurrentRemote we saw)
	}
	byPeer := make(map[string]*merged, len(hosts))
	order := make([]string, 0, len(hosts)) // preserve first-seen order for stable output

	for _, h := range hosts {
		var vpnAddr string
		if len(h.VpnAddrs) > 0 {
			vpnAddr = h.VpnAddrs[0].String()
		}
		if vpnAddr == "" {
			continue
		}
		entry, exists := byPeer[vpnAddr]
		if !exists {
			entry = &merged{}
			byPeer[vpnAddr] = entry
			order = append(order, vpnAddr)
		}
		if h.CurrentRemote.IsValid() && !entry.direct {
			entry.direct = true
			entry.remoteAddr = h.CurrentRemote.String()
		}
		if len(h.CurrentRelaysToMe) > 0 {
			entry.hasRelay = true
		}
	}

	peers = make([]PeerDetail, 0, len(byPeer))
	for _, vpnAddr := range order {
		entry := byPeer[vpnAddr]
		switch {
		case entry.direct:
			direct++
			pd := PeerDetail{
				VpnAddr:    vpnAddr,
				Direct:     true,
				RemoteAddr: entry.remoteAddr,
			}
			if rtt, n := pq.snapshot(vpnAddr); n > 0 {
				pd.RTTms = rtt
			}
			peers = append(peers, pd)
		case entry.hasRelay:
			relayed++
			peers = append(peers, PeerDetail{VpnAddr: vpnAddr, Direct: false})
		default:
			// Pure ghost — no remote address, no relay. Skip.
			continue
		}
		if len(peers) >= maxPeersPerReport {
			break
		}
	}
	return direct, relayed, peers, true
}
