package client

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// way is a path between two machines a and b, like the one through a router: each
// sees the other at an address of the way.
type way struct {
	atA, atB netip.AddrPort // where a sees b, and b sees a
	ab, ba   oneWay
}

type oneWay struct {
	cut   atomic.Bool
	tried atomic.Int64 // packets, also those that were cut
	slow  atomic.Bool  // by 20 ms
}

// newWay makes a path between the two ports, with its own address on host.
func newWay(t *testing.T, host string, portA, portB int) *way {
	t.Helper()
	var conns [2]net.PacketConn
	for i := range conns {
		c, err := net.ListenPacket("udp4", host+":0")
		if err != nil {
			t.Skipf("no second loopback address: %v", err)
		}
		t.Cleanup(func() { c.Close() })
		conns[i] = c
	}
	w := &way{
		atA: conns[0].LocalAddr().(*net.UDPAddr).AddrPort(),
		atB: conns[1].LocalAddr().(*net.UDPAddr).AddrPort(),
	}
	carry := func(d *oneWay, in, out net.PacketConn, to int) {
		buf := make([]byte, 65536)
		for {
			n, _, err := in.ReadFrom(buf)
			if err != nil {
				return
			}
			d.tried.Add(1)
			if d.slow.Load() {
				time.Sleep(20 * time.Millisecond)
			}
			if !d.cut.Load() {
				out.WriteTo(buf[:n], &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: to})
			}
		}
	}
	go carry(&w.ab, conns[0], conns[1], portB)
	go carry(&w.ba, conns[1], conns[0], portA)
	return w
}

// twoWays is a and b, with two paths between them, the second of which works from
// a to b only. a knows those of the paths that are listed.
type twoWays struct {
	t            *testing.T
	m            *e2eMesh
	ipA, ipB     string
	denied       string // by b's lighthouse.remote_allow_list
	good, bad    *way
	portA, portB int
}

func newTwoWays(t *testing.T, badHost string) *twoWays {
	m := newE2EMesh(t)
	w := &twoWays{t: t, m: m, ipA: e2eSubnet + "6", ipB: e2eSubnet + "7", portA: freeUDPPort(t), portB: freeUDPPort(t)}
	w.good = newWay(t, "127.0.0.1", w.portA, w.portB)
	w.bad = newWay(t, badHost, w.portA, w.portB)
	w.bad.ba.cut.Store(true)
	return w
}

func (w *twoWays) start(known ...*way) {
	w.t.Helper()
	var addrs []string
	for _, k := range known {
		addrs = append(addrs, k.atA.String())
	}
	w.m.enroll("a", w.ipA, w.portA, time.Hour, map[string][]string{w.ipB: addrs})
	w.m.enroll("b", w.ipB, w.portB, time.Hour, nil)
	for _, name := range []string{"a", "b"} { // the second loopback address is our "LAN"
		file := filepath.Join(w.m.dir, name, "nebula.yaml")
		yaml, err := os.ReadFile(file)
		if err == nil {
			text := string(yaml) + "preferred_ranges: [127.0.1.0/24]\n"
			if name == "b" && w.denied != "" {
				text = strings.Replace(text, "lighthouse: {", "lighthouse: {remote_allow_list: {"+w.denied+": false, 0.0.0.0/0: true}, ", 1)
			}
			err = os.WriteFile(file, []byte(text), 0o600)
		}
		if err != nil {
			w.t.Fatal(err)
		}
	}
	if err := w.m.c.Start(context.Background()); err != nil {
		w.t.Fatal(err)
	}
}

func (w *twoWays) talk() bool {
	return keepaliveProbe(w.m.c.instances.get("a"), fmt.Sprintf("%s:%d", w.ipB, agentAPIPort)) == nil
}

// remote is where name sends its packets to the peer.
func (w *twoWays) remote(name, peer string) netip.AddrPort {
	h := w.m.c.instances.get(name).control().GetHostInfoByVpnAddr(netip.MustParseAddr(peer), false)
	if h == nil {
		return netip.AddrPort{}
	}
	return h.CurrentRemote
}

// wantOn talks in the meantime: that is what makes the one follow the other.
func (w *twoWays) wantOn(what string, way *way) {
	w.t.Helper()
	within(w.t, 5*time.Second, what, func() bool {
		return w.talk() && w.remote("a", w.ipB) == way.atA && w.remote("b", w.ipA) == way.atB
	})
}

// TestE2E_QuestionMovesNobody: whoever is asked on another path answers there and
// stays where it is, because it can't know whether the answer arrives (vendor patch
// 35). Nebula moved first, so that a question on a path that works in one direction
// only cut the connection.
func TestE2E_QuestionMovesNobody(t *testing.T) {
	w := newTwoWays(t, "127.0.0.1")
	w.start(w.good)
	within(t, 20*time.Second, "a to reach b", w.talk)
	w.wantOn("both on the path they know", w.good)

	ask := func() {
		w.m.c.instances.get("a").control().ProbeEndpoint(netip.MustParseAddr(w.ipB), w.bad.atA)
	}
	ask()
	within(t, 5*time.Second, "b to answer where it was asked", func() bool { return w.bad.ba.tried.Load() > 0 })
	for range 20 {
		if got := w.remote("b", w.ipA); got != w.good.atB {
			t.Fatalf("b sends to %s, want %s: it can't know that a hears it there", got, w.good.atB)
		}
		if !w.talk() {
			t.Fatal("the connection broke")
		}
	}

	w.bad.ba.cut.Store(false)
	ask()
	within(t, 5*time.Second, "a to move: it got an answer", func() bool { return w.remote("a", w.ipB) == w.bad.atA })
	if !w.talk() {
		t.Error("the connection broke when the path changed")
	}
	w.wantOn("b to follow a", w.bad)
}

// TestE2E_StartsWhereBothHear: a handshake goes to all addresses of a peer, and
// the first copy to arrive may have come on a path that doesn't go back. What
// counts is where the other side heard the answer.
func TestE2E_StartsWhereBothHear(t *testing.T) {
	w := newTwoWays(t, "127.0.1.1")
	w.good.ab.slow.Store(true)
	w.start(w.good, w.bad)
	start := time.Now()
	within(t, 20*time.Second, "a to reach b", w.talk)
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("a reached b after %s", took)
	}
	if w.bad.ba.tried.Load() == 0 {
		t.Fatal("the handshake did not come on the path that doesn't go back")
	}
	w.wantOn("both on the path that works", w.good)
}

// TestE2E_MeetOnTheLAN: the copy of a handshake that comes on our LAN is a reason
// to ask there, not to move there.
func TestE2E_MeetOnTheLAN(t *testing.T) {
	for _, works := range []bool{true, false} {
		t.Run(fmt.Sprintf("answers come back %v", works), func(t *testing.T) {
			w := newTwoWays(t, "127.0.1.1")
			w.bad.ba.cut.Store(!works)
			w.bad.ab.slow.Store(true) // so it comes when the tunnel is there
			w.start(w.good, w.bad)
			within(t, 20*time.Second, "a to reach b", w.talk)
			if works {
				w.wantOn("both on the LAN", w.bad)
				return
			}
			for start := time.Now(); time.Since(start) < time.Second; {
				if !w.talk() {
					t.Fatal("the connection broke")
				}
			}
			if w.bad.ba.tried.Load() < 2 { // the first is the answer to the handshake
				t.Error("b did not ask on the LAN")
			}
			w.wantOn("both where they hear each other", w.good)
		})
	}
}

// TestE2E_NoAnswerToADeniedAddress: lighthouse.remote_allow_list goes for the
// answer to a question too.
func TestE2E_NoAnswerToADeniedAddress(t *testing.T) {
	for _, denied := range []string{"", "127.0.1.0/24"} {
		t.Run("denied "+denied, func(t *testing.T) {
			w := newTwoWays(t, "127.0.1.1")
			w.denied = denied
			w.start(w.good)
			within(t, 20*time.Second, "a to reach b", w.talk)
			w.m.c.instances.get("a").control().ProbeEndpoint(netip.MustParseAddr(w.ipB), w.bad.atA)
			within(t, 5*time.Second, "the question to arrive", func() bool { return w.bad.ab.tried.Load() > 0 })
			for range 20 {
				w.talk()
			}
			if answered := w.bad.ba.tried.Load() > 0; answered != (denied == "") {
				t.Errorf("answered: %v", answered)
			}
		})
	}
}

// TestE2E_NewTunnelStays: what came in on the old tunnel before the new one is no
// reason to go back to it (vendor patch 36). Nebula went back for up to 10 s, and a
// new tunnel is how a broken path is repaired.
func TestE2E_NewTunnelStays(t *testing.T) {
	w := newTwoWays(t, "127.0.0.1")
	w.start(w.good)
	within(t, 20*time.Second, "a to reach b", w.talk)
	tunnel := func() uint32 {
		return w.m.c.instances.get("a").control().GetHostInfoByVpnAddr(netip.MustParseAddr(w.ipB), false).LocalIndex
	}
	old := tunnel()
	for start := time.Now(); time.Since(start) < 3*time.Second; time.Sleep(50 * time.Millisecond) {
		w.talk()
	}
	w.m.c.instances.get("b").control().CreateTunnel(netip.MustParseAddr(w.ipA))
	within(t, 5*time.Second, "a new tunnel", func() bool { return w.talk() && tunnel() != old })

	// 10 s after it was made, and every 10 s, each of the two may become the one in
	// use: the old one 3 s earlier than the new one.
	for start := time.Now(); time.Since(start) < 11*time.Second; time.Sleep(50 * time.Millisecond) {
		if !w.talk() {
			t.Fatal("a doesn't reach b")
		}
		if tunnel() == old {
			t.Fatalf("a went back to the old tunnel after %s", time.Since(start).Round(time.Second))
		}
	}
}
