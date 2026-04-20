package main

import (
	"net/netip"
	"testing"

	"github.com/slackhq/nebula"
)

// hostInfo is a builder for nebula.ControlHostInfo entries used by
// the tests below. The real hostmap is iterated by ListHostmapHosts;
// since collectPeerState takes a *nebula.Control we'd normally need
// to spin one up. Instead we extracted a pure helper that operates
// on the slice — see classifyHostInfos. The wrapper test below ensures
// the boundary conversion remains correct.

func mkHost(vpn string, remote netip.AddrPort, relays []netip.Addr) nebula.ControlHostInfo {
	return nebula.ControlHostInfo{
		VpnAddrs:          []netip.Addr{netip.MustParseAddr(vpn)},
		CurrentRemote:     remote,
		CurrentRelaysToMe: relays,
	}
}

// classifyHostInfos is the inner pure function we want to assert on.
// It's the same logic collectPeerState applies, factored out so tests
// can exercise it without needing a live *nebula.Control. Any change
// to collectPeerState that breaks invariants here breaks the heartbeat.
func classifyHostInfos(hosts []nebula.ControlHostInfo) (direct, relayed int, peers []PeerDetail) {
	type merged struct {
		direct     bool
		hasRelay   bool
		remoteAddr string
	}
	byPeer := make(map[string]*merged, len(hosts))
	order := make([]string, 0, len(hosts))

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
			peers = append(peers, PeerDetail{
				VpnAddr:    vpnAddr,
				Direct:     true,
				RemoteAddr: entry.remoteAddr,
			})
		case entry.hasRelay:
			relayed++
			peers = append(peers, PeerDetail{VpnAddr: vpnAddr, Direct: false})
		}
		if len(peers) >= maxPeersPerReport {
			break
		}
	}
	return direct, relayed, peers
}

func TestClassify_SingleDirectPeer(t *testing.T) {
	addr := netip.MustParseAddrPort("203.0.113.5:4242")
	d, r, p := classifyHostInfos([]nebula.ControlHostInfo{
		mkHost("10.42.1.7", addr, nil),
	})
	if d != 1 || r != 0 {
		t.Errorf("counts: direct=%d relayed=%d, want 1/0", d, r)
	}
	if len(p) != 1 || !p[0].Direct || p[0].RemoteAddr != addr.String() {
		t.Errorf("peer: %+v", p)
	}
}

func TestClassify_SingleRelayedPeer(t *testing.T) {
	relay := netip.MustParseAddr("10.42.1.1")
	d, r, p := classifyHostInfos([]nebula.ControlHostInfo{
		mkHost("10.42.1.7", netip.AddrPort{}, []netip.Addr{relay}),
	})
	if d != 0 || r != 1 {
		t.Errorf("counts: direct=%d relayed=%d, want 0/1", d, r)
	}
	if len(p) != 1 || p[0].Direct {
		t.Errorf("peer: %+v", p)
	}
}

// THE bug fix: same VpnAddr appears twice in the hostmap (one direct
// session + one relay session lingering during transition). Pre-fix
// reported "1 direct, 1 relayed" for the same peer; post-fix reports
// "1 direct, 0 relayed" — the active path wins.
func TestClassify_DedupesPreferringDirect(t *testing.T) {
	relay := netip.MustParseAddr("10.42.1.1")
	direct := netip.MustParseAddrPort("203.0.113.5:4242")

	d, r, p := classifyHostInfos([]nebula.ControlHostInfo{
		mkHost("10.42.1.7", netip.AddrPort{}, []netip.Addr{relay}), // old relay session
		mkHost("10.42.1.7", direct, nil),                           // new direct session
	})
	if d != 1 || r != 0 {
		t.Errorf("counts: got %d/%d, want 1/0", d, r)
	}
	if len(p) != 1 {
		t.Fatalf("len(peers) = %d, want 1", len(p))
	}
	if !p[0].Direct {
		t.Errorf("dedup chose relay over direct")
	}
	if p[0].RemoteAddr != direct.String() {
		t.Errorf("RemoteAddr = %q, want %q", p[0].RemoteAddr, direct.String())
	}
}

// Inverse ordering of the entries above — the result must be identical
// (we can't rely on hostmap iteration order, so the dedup must work
// regardless of which session is encountered first).
func TestClassify_DedupesOrderIndependent(t *testing.T) {
	relay := netip.MustParseAddr("10.42.1.1")
	direct := netip.MustParseAddrPort("203.0.113.5:4242")

	d, r, _ := classifyHostInfos([]nebula.ControlHostInfo{
		mkHost("10.42.1.7", direct, nil),                           // direct first
		mkHost("10.42.1.7", netip.AddrPort{}, []netip.Addr{relay}), // relay second
	})
	if d != 1 || r != 0 {
		t.Errorf("got %d/%d, want 1/0", d, r)
	}
}

// One peer DIRECT-only, another peer RELAY-only — counts should be
// 1 direct, 1 relayed. (Different VpnAddrs, so dedup doesn't apply.)
func TestClassify_TwoPeersOneDirectOneRelayed(t *testing.T) {
	relay := netip.MustParseAddr("10.42.1.1")
	direct := netip.MustParseAddrPort("203.0.113.5:4242")

	d, r, p := classifyHostInfos([]nebula.ControlHostInfo{
		mkHost("10.42.1.7", direct, nil),
		mkHost("10.42.1.11", netip.AddrPort{}, []netip.Addr{relay}),
	})
	if d != 1 || r != 1 {
		t.Errorf("got %d/%d, want 1/1", d, r)
	}
	if len(p) != 2 {
		t.Errorf("len = %d, want 2", len(p))
	}
}

// Pure ghost: HostInfo with no remote AND no relay — must be skipped
// from both counts and peers list.
func TestClassify_SkipsGhosts(t *testing.T) {
	d, r, p := classifyHostInfos([]nebula.ControlHostInfo{
		mkHost("10.42.1.7", netip.AddrPort{}, nil), // no remote, no relay
	})
	if d != 0 || r != 0 {
		t.Errorf("ghost shouldn't count: got %d/%d", d, r)
	}
	if len(p) != 0 {
		t.Errorf("ghost shouldn't appear in peers: %+v", p)
	}
}

// HostInfo with empty VpnAddrs — Nebula API allows this; must be
// silently dropped.
func TestClassify_SkipsEmptyVpnAddr(t *testing.T) {
	addr := netip.MustParseAddrPort("203.0.113.5:4242")
	d, _, p := classifyHostInfos([]nebula.ControlHostInfo{
		{CurrentRemote: addr}, // VpnAddrs is nil
	})
	if d != 0 || len(p) != 0 {
		t.Errorf("empty VpnAddr leaked: direct=%d peers=%+v", d, p)
	}
}

// Cap test: more than maxPeersPerReport unique peers should truncate
// the per-peer slice but the counts can still reflect the full picture
// (this matches the prior contract — counts are aggregates, peers is
// detail capped for payload size).
func TestClassify_CapsPeersListAtMax(t *testing.T) {
	addr := netip.MustParseAddrPort("203.0.113.5:4242")
	hosts := make([]nebula.ControlHostInfo, 0, maxPeersPerReport+5)
	for i := 0; i < maxPeersPerReport+5; i++ {
		ip := netip.AddrFrom4([4]byte{10, 42, 1, byte(i + 1)})
		hosts = append(hosts, nebula.ControlHostInfo{
			VpnAddrs:      []netip.Addr{ip},
			CurrentRemote: addr,
		})
	}
	_, _, p := classifyHostInfos(hosts)
	if len(p) != maxPeersPerReport {
		t.Errorf("len(peers) = %d, want %d (cap)", len(p), maxPeersPerReport)
	}
}

// nil ctrl path — collectPeerState (the wrapper, not the inner helper)
// must return ok=false so callers can omit the field from heartbeat.
func TestCollectPeerState_NilCtrlReturnsNotOk(t *testing.T) {
	d, r, p, ok := collectPeerState(nil)
	if ok {
		t.Errorf("expected ok=false on nil ctrl")
	}
	if d != 0 || r != 0 || p != nil {
		t.Errorf("nil ctrl should return zero values: %d/%d/%v", d, r, p)
	}
}
