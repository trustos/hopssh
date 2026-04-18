package main

import (
	"github.com/slackhq/nebula"
)

// collectPeerState queries Nebula's hostmap and summarizes the agent's
// current peers as two counts:
//
//   - direct:  peer has an active CurrentRemote AND no CurrentRelaysToMe
//     (UDP direct / hole-punched P2P).
//   - relayed: peer is reached via at least one relay (CurrentRelaysToMe
//     is non-empty). This is the lighthouse-as-relay path used when
//     hole punching fails (symmetric NAT, CGNAT, restrictive firewalls).
//
// Peers with neither a valid CurrentRemote nor any relay are skipped —
// they're stale hostmap entries where no connection is currently
// established (e.g., right after a long sleep, before handshake
// completes).
//
// ok is false when ctrl is nil (mesh not started, or agent starting
// up) — callers should omit peer fields from the heartbeat in that
// case rather than report zeros that would overwrite the last known
// good values on the server.
func collectPeerState(ctrl *nebula.Control) (direct, relayed int, ok bool) {
	if ctrl == nil {
		return 0, 0, false
	}
	hosts := ctrl.ListHostmapHosts(false)
	for _, h := range hosts {
		switch {
		case len(h.CurrentRelaysToMe) > 0:
			relayed++
		case h.CurrentRemote.IsValid():
			direct++
		}
	}
	return direct, relayed, true
}
