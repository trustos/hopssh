package client

// Keeps the path to every peer working, and direct where that is possible.
//
// Nebula leaves both to chance. When two devices meet, the handshake over the
// relay often overtakes the direct one, and then the connection stays on the
// relay until 1000 packets have been sent. And a path that went dead (the router
// got a new address, the relay restarted, the laptop is in another network that
// gave it the same address) is noticed after 40 s.
//
// Two machines of one LAN that met at the outside address of their router stay
// there, too: that its own address is in the range of the LAN fools Nebula.
//
// Two things it must not do: close a tunnel, and look for another path to a peer
// whose direct path works, unless the peer is on our LAN and we go another way.
// Looking everywhere made paths flap in v0.10.28. A peer is asked on another path
// and nobody moves before the answer is here (vendor patch 35).
//
// Tested against real networks in internal/netlab.

import (
	"context"
	"log"
	"net"
	"net/netip"
	"runtime/debug"
	"slices"
	"sync/atomic"
	"time"

	"github.com/slackhq/nebula"
)

const (
	pathTick      = time.Second
	pathTickLimit = 5 * time.Second // like the rebind in watchNetworkChanges

	// A peer that was sent something and is silent: ask it, then shake hands
	// again, next to the tunnel that is there. The side with the direct path goes
	// first, because a direct path that works in one direction only is repaired
	// from its blind end.
	pathAskAfter          = 1 * time.Second
	pathShakeAfter        = 4 * time.Second
	pathShakeAfterRelayed = 6 * time.Second
	pathShakeMin          = 2 * time.Second
	pathShakeMax          = 30 * time.Second

	// How often to look for a direct path to a peer on the relay, and for the LAN.
	pathDirectMin   = 1 * time.Second
	pathDirectMax   = time.Minute
	pathDirectReset = time.Minute // of a direct path
)

// pathControl is what it needs of *nebula.Control.
type pathControl interface {
	ListHostmapHosts(pendingMap bool) []nebula.ControlHostInfo
	// ProbeEndpoint asks the peer for an answer, which comes the way the question
	// went; the zero address means the path that is in use.
	ProbeEndpoint(vpnAddr netip.Addr, remote netip.AddrPort) bool
	// QueryLighthouse makes the lighthouse tell the peer to open its router for us.
	QueryLighthouse(vpnAddr netip.Addr) *nebula.CacheMap
	CreateTunnel(vpnAddr netip.Addr)
}

type pathWatch struct {
	name  string
	nets  func() []netip.Prefix // our addresses, with the size of the network
	peers map[netip.Addr]*peerPath
	start time.Time   // the first tick
	busy  atomic.Bool // a tick is under way
}

type peerPath struct {
	index       uint32 // of the tunnel that the counters belong to
	sent, heard uint64
	silentSince time.Time // something was sent since, and nothing came back

	nextShake time.Time
	shakeWait time.Duration

	directSince time.Time
	nextDirect  time.Time
	directWait  time.Duration
	asked       bool // the lighthouse; now try the addresses

	nextLAN time.Time
	lanWait time.Duration
}

func runPathWatch(ctx context.Context, inst *meshInstance) {
	t := time.NewTicker(pathTick)
	defer t.Stop()
	w := pathWatch{name: inst.name(), nets: ownNets}
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			var ctrl pathControl
			if c := inst.control(); c != nil {
				ctrl = c
			}
			w.step(inst, ctrl, now)
		}
	}
}

// step runs one tick and stamps it for the watchdog. After a tick that hangs or
// panics it does neither, until the tick has come to its end.
func (w *pathWatch) step(inst *meshInstance, ctrl pathControl, now time.Time) {
	if w.busy.Load() {
		return
	}
	inst.markPathActivity()
	if ctrl == nil {
		return
	}
	w.busy.Store(true)
	runWithTimeout(w.name, "path watch", pathTickLimit, func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[path %s] CRITICAL: panic: %v\n%s", w.name, r, debug.Stack())
			}
		}()
		w.tick(ctrl, now)
		w.busy.Store(false)
	})
}

func (w *pathWatch) tick(ctrl pathControl, now time.Time) {
	// All waits are whole seconds, and a tick that comes a microsecond early
	// must not cost another one.
	if w.start.IsZero() {
		w.start = now
	}
	now = w.start.Add(now.Sub(w.start).Round(pathTick))

	hosts := ctrl.ListHostmapHosts(false)
	nets := w.nets()
	peers := make(map[netip.Addr]*peerPath, len(hosts))
	for _, h := range hosts {
		if len(h.VpnAddrs) == 0 {
			continue
		}
		// The lighthouse is a peer like any other: unlike keepalive.go, which asks
		// the agent, this asks Nebula, and that answers there too.
		addr := h.VpnAddrs[0]
		p := w.peers[addr]
		if p == nil {
			p = &peerPath{}
		}
		peers[addr] = p
		w.silence(ctrl, addr, h, p, now)
		w.relayed(ctrl, addr, h, p, now)
		w.lan(ctrl, addr, h, p, nets, now)
	}
	w.peers = peers // forgets those without a tunnel
}

// silence repairs a path on which nothing comes back.
func (w *pathWatch) silence(ctrl pathControl, addr netip.Addr, h nebula.ControlHostInfo, p *peerPath, now time.Time) {
	direct := h.CurrentRemote.IsValid()
	heard := h.ReceivedDirect
	if !direct {
		heard += h.ReceivedRelayed
	}
	switch {
	case h.LocalIndex != p.index: // another tunnel
		p.index, p.sent, p.heard, p.silentSince = h.LocalIndex, h.MessageCounter, heard, time.Time{}
	case heard != p.heard:
		p.sent, p.heard, p.silentSince = h.MessageCounter, heard, time.Time{}
		p.shakeWait, p.nextShake = 0, time.Time{}
	case h.MessageCounter != p.sent && p.silentSince.IsZero():
		p.silentSince = now
	}

	var silent time.Duration
	if !p.silentSince.IsZero() {
		silent = now.Sub(p.silentSince)
	}
	if silent >= pathAskAfter {
		ctrl.ProbeEndpoint(addr, netip.AddrPort{})
	}

	limit := pathShakeAfterRelayed
	if direct {
		limit = pathShakeAfter
	} else if len(h.CurrentRelaysToMe) == 0 {
		limit = 0 // there is no path at all
	}
	if silent < limit || now.Before(p.nextShake) {
		return
	}
	if p.shakeWait == 0 {
		log.Printf("[path %s] %s: nothing back for %s, shaking hands again", w.name, addr, silent.Round(time.Second))
	}
	p.shakeWait = backOff(p.shakeWait, pathShakeMin, pathShakeMax)
	p.nextShake = now.Add(p.shakeWait)
	ctrl.CreateTunnel(addr)
}

// relayed looks for a direct path to a peer on the relay.
func (w *pathWatch) relayed(ctrl pathControl, addr netip.Addr, h nebula.ControlHostInfo, p *peerPath, now time.Time) {
	if h.CurrentRemote.IsValid() {
		p.asked = false
		if p.directSince.IsZero() {
			p.directSince = now
		}
		if now.Sub(p.directSince) >= pathDirectReset {
			p.directWait, p.nextDirect = 0, time.Time{}
		}
		return
	}
	p.directSince = time.Time{}
	if p.asked {
		p.asked = false
		for _, remote := range h.RemoteAddrs {
			if remote.Addr().Is4() { // what Nebula listens on
				ctrl.ProbeEndpoint(addr, remote)
			}
		}
	} else if !now.Before(p.nextDirect) {
		p.directWait = backOff(p.directWait, pathDirectMin, pathDirectMax)
		p.nextDirect = now.Add(p.directWait)
		p.asked = true
		ctrl.QueryLighthouse(addr)
	}
}

// lan looks for a peer on our LAN that we reach directly, but not there.
func (w *pathWatch) lan(ctrl pathControl, addr netip.Addr, h nebula.ControlHostInfo, p *peerPath, nets []netip.Prefix, now time.Time) {
	if !h.CurrentRemote.IsValid() {
		return
	}
	ours := func(a netip.AddrPort) bool {
		return slices.ContainsFunc(nets, func(n netip.Prefix) bool { return n.Contains(a.Addr()) && n.Addr() != a.Addr() })
	}
	if !slices.ContainsFunc(h.RemoteAddrs, func(a netip.AddrPort) bool { return a != h.CurrentRemote && ours(a) }) {
		p.lanWait, p.nextLAN = 0, time.Time{}
		return
	}
	if now.Before(p.nextLAN) {
		return
	}
	p.lanWait = backOff(p.lanWait, pathDirectMin, pathDirectMax)
	p.nextLAN = now.Add(p.lanWait)

	// Only where the peer says it is. Where it was seen may be the router.
	var there []netip.AddrPort
	if cache := ctrl.QueryLighthouse(addr); cache != nil {
		for _, c := range *cache {
			there = append(there, c.Reported...)
		}
	}
	there = slices.DeleteFunc(there, func(a netip.AddrPort) bool { return !ours(a) })
	if slices.Contains(there, h.CurrentRemote) {
		return
	}
	for _, a := range there {
		ctrl.ProbeEndpoint(addr, a)
	}
}

func ownNets() []netip.Prefix {
	addrs, _ := net.InterfaceAddrs()
	var nets []netip.Prefix
	for _, a := range addrs {
		if n, err := netip.ParsePrefix(a.String()); err == nil && n.Addr().Is4() && !n.Addr().IsLoopback() {
			nets = append(nets, n)
		}
	}
	return nets
}

// backOff doubles the wait, from lo up to hi.
func backOff(wait, lo, hi time.Duration) time.Duration {
	switch wait *= 2; {
	case wait < lo:
		return lo
	case wait > hi:
		return hi
	}
	return wait
}
