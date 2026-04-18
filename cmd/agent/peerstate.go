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
}

// maxPeersPerReport caps the reported per-peer slice to prevent
// pathological payload growth on large meshes. 100 covers any realistic
// fleet; beyond that the aggregate counts still tell the story.
const maxPeersPerReport = 100

// collectPeerState queries Nebula's hostmap and summarizes the agent's
// current peers:
//
//   - direct:  peer has a valid CurrentRemote AND no CurrentRelaysToMe
//     (UDP direct / hole-punched P2P).
//   - relayed: peer is reached via at least one relay (CurrentRelaysToMe
//     non-empty). This is the lighthouse-as-relay path used when hole
//     punching fails (symmetric NAT, CGNAT, restrictive firewalls).
//   - peers:   per-peer detail, one entry per classified host above.
//     Capped at maxPeersPerReport.
//
// Peers with neither a valid CurrentRemote nor any relay are skipped —
// stale hostmap entries where no connection is currently established.
//
// ok is false when ctrl is nil (mesh not started, or agent starting
// up) — callers omit peer fields from the heartbeat in that case so
// the server's COALESCE preserves the last known good value rather
// than overwriting with zeros/empty.
func collectPeerState(ctrl *nebula.Control) (direct, relayed int, peers []PeerDetail, ok bool) {
	if ctrl == nil {
		return 0, 0, nil, false
	}
	hosts := ctrl.ListHostmapHosts(false)
	peers = make([]PeerDetail, 0, len(hosts))
	for _, h := range hosts {
		var vpnAddr string
		if len(h.VpnAddrs) > 0 {
			vpnAddr = h.VpnAddrs[0].String()
		}
		if vpnAddr == "" {
			continue
		}
		switch {
		case len(h.CurrentRelaysToMe) > 0:
			relayed++
			peers = append(peers, PeerDetail{VpnAddr: vpnAddr, Direct: false})
		case h.CurrentRemote.IsValid():
			direct++
			peers = append(peers, PeerDetail{
				VpnAddr:    vpnAddr,
				Direct:     true,
				RemoteAddr: h.CurrentRemote.String(),
			})
		}
		if len(peers) >= maxPeersPerReport {
			break
		}
	}
	return direct, relayed, peers, true
}
