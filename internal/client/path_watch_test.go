package client

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/slackhq/nebula"
)

var (
	pathPeer  = netip.MustParseAddr("10.42.1.7")
	pathRelay = netip.MustParseAddr("10.42.1.1")
	pathWAN   = netip.MustParseAddrPort("203.0.113.22:4242")
	pathLAN   = netip.MustParseAddrPort("192.168.1.50:4242")

	pathNets   = []netip.Prefix{netip.MustParsePrefix("192.168.1.51/24"), netip.MustParsePrefix("10.42.1.8/24")}
	pathRouter = netip.MustParseAddrPort("192.168.1.1:4242")
)

// fakePaths is a Nebula with one peer, and writes down what it is asked for.
type fakePaths struct {
	host     *nebula.ControlHostInfo
	reported []netip.AddrPort
	calls    []string
}

func (f *fakePaths) ListHostmapHosts(bool) []nebula.ControlHostInfo {
	if f.host == nil {
		return nil
	}
	return []nebula.ControlHostInfo{*f.host}
}

func (f *fakePaths) ProbeEndpoint(_ netip.Addr, remote netip.AddrPort) bool {
	f.host.MessageCounter++ // a real one counts it too
	if remote.IsValid() {
		f.calls = append(f.calls, "try "+remote.String())
	} else {
		f.calls = append(f.calls, "ask")
	}
	return true
}

func (f *fakePaths) QueryLighthouse(netip.Addr) *nebula.CacheMap {
	f.calls = append(f.calls, "query")
	return &nebula.CacheMap{
		pathPeer.String():  {Reported: f.reported, Learned: []netip.AddrPort{pathRouter}},
		pathRelay.String(): {Reported: f.reported[:min(1, len(f.reported))]},
	}
}

func (f *fakePaths) CreateTunnel(netip.Addr) { f.calls = append(f.calls, "shake") }

type pathRun struct {
	t *testing.T
	fakePaths
	w   pathWatch
	now time.Time
}

func newPathRun(t *testing.T, direct bool) *pathRun {
	r := &pathRun{t: t, now: time.Now()}
	r.w.nets = func() []netip.Prefix { return []netip.Prefix{netip.MustParsePrefix("10.64.0.50/24")} }
	r.host = &nebula.ControlHostInfo{VpnAddrs: []netip.Addr{pathPeer}, LocalIndex: 1, MessageCounter: 2, RemoteAddrs: []netip.AddrPort{pathLAN, pathWAN, netip.MustParseAddrPort("[2001:db8::22]:4242")}}
	if direct {
		r.host.CurrentRemote = pathWAN
	} else {
		r.host.CurrentRelaysToMe = []netip.Addr{pathRelay}
	}
	return r
}

// ticks runs n ticks and returns what each one did: "-" is nothing.
func (r *pathRun) ticks(n int, before func()) string {
	var out []string
	for range n {
		if before != nil {
			before()
		}
		r.calls = nil
		r.now = r.now.Add(pathTick)
		r.w.tick(r, r.now)
		out = append(out, strings.Join(append(r.calls, "-")[:max(len(r.calls), 1)], "+"))
	}
	return strings.Join(out, " ")
}

func (r *pathRun) want(what, got, want string) {
	r.t.Helper()
	if got != want {
		r.t.Errorf("%s:\n got %s\nwant %s", what, got, want)
	}
}

func (r *pathRun) talk()   { r.host.MessageCounter++; r.host.ReceivedDirect++ }
func (r *pathRun) shout()  { r.host.MessageCounter++ }
func (r *pathRun) relays() { r.host.MessageCounter++; r.host.ReceivedRelayed++ }

func nothing(n int) string { return strings.TrimSpace(strings.Repeat("- ", n)) }

func TestPathWatch_LeavesAWorkingPathAlone(t *testing.T) {
	r := newPathRun(t, true)
	r.want("in use", r.ticks(200, r.talk), nothing(200))
	r.want("idle", r.ticks(200, nil), nothing(200))
	r.want("only receiving", r.ticks(200, func() { r.host.ReceivedDirect++ }), nothing(200))
}

func TestPathWatch_SilentPeer(t *testing.T) {
	r := newPathRun(t, true)
	r.ticks(5, r.talk)
	r.want("silent", r.ticks(14, r.shout), "- ask ask ask ask+shake ask ask+shake ask ask ask ask+shake ask ask ask")
	if n := strings.Count(r.ticks(120, r.shout), "shake"); n != 5 {
		t.Errorf("%d handshakes in the next 2 min, want 5: 8, 16, then 30 s apart", n)
	}
	r.want("an answer", r.ticks(3, r.talk), nothing(3))
	r.want("silent again", r.ticks(5, r.shout), "- ask ask ask ask+shake")
}

// The side with the direct path goes first, so the relayed one waits 2 s longer.
func TestPathWatch_SilentPeerOnTheRelay(t *testing.T) {
	r := newPathRun(t, false)
	r.host.RemoteAddrs = nil // nowhere to look for a direct path
	ticks := func(n int, before func()) string {
		return strings.ReplaceAll(strings.ReplaceAll(r.ticks(n, before), "+query", ""), "query", "-")
	}
	ticks(5, r.relays)
	r.want("silent", ticks(9, r.shout), "- ask ask ask ask ask ask+shake ask ask+shake")
	r.want("an answer", ticks(3, r.relays), nothing(3))
}

func TestPathWatch_OneLostPacketIsNotSilence(t *testing.T) {
	r := newPathRun(t, true)
	r.ticks(5, r.talk)
	r.want("one packet without an answer", r.ticks(1, r.shout)+" "+r.ticks(1, nil), "- ask")
	r.want("the answer to the question", r.ticks(5, r.talk), nothing(5))
}

// What comes over the relay says nothing about a direct path: it may work in one
// direction only.
func TestPathWatch_DirectPathThatBringsNothingBack(t *testing.T) {
	r := newPathRun(t, true)
	r.ticks(5, r.talk)
	r.want("answers come over the relay", r.ticks(5, r.relays), "- ask ask ask ask+shake")
}

func TestPathWatch_NewTunnelStartsAfresh(t *testing.T) {
	r := newPathRun(t, true)
	r.ticks(5, r.talk)
	r.ticks(4, r.shout)
	r.host.LocalIndex, r.host.MessageCounter, r.host.ReceivedDirect = 2, 2, 0
	r.want("silent on the new tunnel", r.ticks(6, r.shout), "- - ask ask ask ask+shake")
}

func TestPathWatch_Relayed(t *testing.T) {
	r := newPathRun(t, false)
	try := fmt.Sprintf("try %s+try %s", pathLAN, pathWAN)
	r.want("on the relay", r.ticks(9, r.relays), "query "+try+" query "+try+" query "+try+" - - query")
	if got := strings.Count(r.ticks(600, r.relays), "query"); got < 9 || got > 12 {
		t.Errorf("looked for a direct path %d times in 10 min, want about once a minute", got)
	}
}

func TestPathWatch_LooksForADirectPathAfterTheRelayOnly(t *testing.T) {
	r := newPathRun(t, false)
	r.ticks(1, r.relays)
	r.host.CurrentRemote = pathWAN // found by the other side before we tried
	r.want("direct", r.ticks(300, r.talk), nothing(300))
	r.host.CurrentRemote = netip.AddrPort{}
	r.want("on the relay again", r.ticks(1, r.relays), "query")
}

func TestPathWatch_NoPathAtAll(t *testing.T) {
	r := newPathRun(t, false)
	r.host.CurrentRelaysToMe = nil
	got := r.ticks(8, nil)
	if want := "shake+query"; !strings.HasPrefix(got, want) {
		t.Errorf("got %s, want %s first", got, want)
	}
	if n := strings.Count(got, "shake"); n != 3 {
		t.Errorf("shook hands %d times in 8 s, want 3 (0, 2, 6): %s", n, got)
	}
}

func TestPathWatch_ForgetsPeers(t *testing.T) {
	r := newPathRun(t, false)
	r.ticks(100, r.relays)
	h := r.host
	r.host = nil
	r.ticks(1, nil)
	r.host = h
	r.want("back", r.ticks(1, nil), "query")
}

func newLANRun(t *testing.T, at netip.AddrPort) *pathRun {
	r := newPathRun(t, true)
	r.w.nets = func() []netip.Prefix { return pathNets }
	r.host.CurrentRemote = at
	r.host.RemoteAddrs = []netip.AddrPort{pathRouter, pathLAN, pathWAN}
	r.reported = []netip.AddrPort{pathLAN, pathWAN}
	return r
}

func TestPathWatch_LAN(t *testing.T) {
	for _, at := range []netip.AddrPort{pathWAN, pathRouter} {
		r := newLANRun(t, at)
		try := fmt.Sprintf("query+try %s+try %[1]s", pathLAN) // two say so
		r.want("through the router at "+at.String(), r.ticks(8, r.talk), try+" "+try+" - "+try+" - - - "+try)
		if got := strings.Count(r.ticks(600, r.talk), "query"); got < 9 || got > 12 {
			t.Errorf("looked for the LAN %d times in 10 min, want about once a minute", got)
		}
		r.host.CurrentRemote = pathLAN
		r.host.RemoteAddrs = []netip.AddrPort{pathLAN, pathWAN}
		r.want("on the LAN", r.ticks(200, r.talk), nothing(200))
		r.host.CurrentRemote = at
		r.want("through the router again", r.ticks(1, r.talk), try)
	}
}

func TestPathWatch_NotOnOurLAN(t *testing.T) {
	r := newLANRun(t, pathWAN)
	r.w.nets = func() []netip.Prefix { return pathNets[1:] }
	r.want("another network", r.ticks(200, r.talk), nothing(200))

	// Another home with the same addresses.
	r = newLANRun(t, pathWAN)
	r.w.nets = func() []netip.Prefix { return []netip.Prefix{netip.MustParsePrefix("192.168.1.50/24")} }
	r.host.RemoteAddrs = []netip.AddrPort{pathLAN, pathWAN}
	r.want("that is our own address", r.ticks(200, r.talk), nothing(200))

	// Where it was seen is not where it says it is.
	r = newLANRun(t, pathLAN)
	if got := r.ticks(200, r.talk); strings.Contains(got, "try") {
		t.Errorf("on the LAN, but tried: %s", got)
	}

	r = newLANRun(t, netip.AddrPort{})
	r.host.CurrentRelaysToMe = []netip.Addr{pathRelay}
	if got := r.ticks(1, r.relays); got != "query" {
		t.Errorf("on the relay: %s, want query, once", got)
	}
}

func TestOwnNets(t *testing.T) {
	for _, n := range ownNets() {
		if !n.Addr().Is4() || n.Addr().IsLoopback() || n.Bits() == 0 {
			t.Errorf("%s", n)
		}
	}
}
