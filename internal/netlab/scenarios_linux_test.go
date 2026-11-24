package netlab

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trustos/hopssh/internal/e2etest"
)

// want waits until a's packets to b take a path that starts with kind.
func want(a, b *Agent, kind string, within time.Duration) {
	a.l.t.Helper()
	start := time.Now()
	var last string
	for time.Since(start) < within {
		if last = a.path(b); strings.HasPrefix(last, kind) {
			a.l.report("%s -> %s: %s after %s", a.Name, b.Name, last, time.Since(start).Round(time.Second))
			return
		}
		time.Sleep(time.Second)
	}
	a.l.t.Fatalf("%s -> %s: %q after %s, want %q", a.Name, b.Name, last, within, kind)
}

// reaches is whether a can open a connection to b over the mesh.
func (a *Agent) reaches(b *Agent, within time.Duration) bool {
	ln := b.Listen(b.MeshIP + ":0")
	for start := time.Now(); time.Since(start) < within; time.Sleep(200 * time.Millisecond) {
		if c, err := a.Dial("tcp", ln.Addr().String(), 5*time.Second); err == nil {
			c.Close()
			return true
		}
	}
	return false
}

// tcp requires that a can open a connection to b over the mesh, and returns
// how long that took.
func tcp(a, b *Agent, within time.Duration) time.Duration {
	a.l.t.Helper()
	ln := b.Listen(b.MeshIP + ":0")
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("hi"))
			c.Close()
		}
	}()
	start := time.Now()
	for {
		c, err := a.Dial("tcp", ln.Addr().String(), min(5*time.Second, within))
		if err == nil {
			c.SetReadDeadline(time.Now().Add(5 * time.Second))
			var b [2]byte
			_, err = io.ReadFull(c, b[:])
			c.Close()
		}
		took := time.Since(start)
		if err == nil {
			a.l.report("%s -> %s: first answer after %s", a.Name, b.Name, took.Round(time.Millisecond))
			return took
		}
		if took > within {
			a.l.t.Fatalf("%s cannot reach %s over the mesh within %s: %v", a.Name, b.Name, within, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// meet requires that a and b reach each other at once, that is without
// runPathWatch, which looks every second.
func meet(a, b *Agent) {
	a.l.t.Helper()
	if took := max(tcp(a, b, 5*time.Second), tcp(b, a, 5*time.Second)); took > 800*time.Millisecond {
		a.l.t.Errorf("the first answer took %s", took)
	}
}

// bulk requires that a can send b n bytes in one go within the time given.
func bulk(a, b *Agent, n int64, within time.Duration) {
	a.l.t.Helper()
	if err := tryBulk(a, b, n, within); err != nil {
		a.l.t.Error(err)
	}
}

func tryBulk(a, b *Agent, n int64, within time.Duration) error {
	ln := b.Listen(b.MeshIP + ":0")
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		if got, _ := io.Copy(io.Discard, c); got == n {
			c.Write([]byte("ok"))
		}
	}()
	c, err := a.Dial("tcp", ln.Addr().String(), 10*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	start := time.Now()
	c.SetDeadline(start.Add(within))
	_, err = io.CopyN(c, zeroes{}, n)
	if err == nil {
		c.(*net.TCPConn).CloseWrite()
		_, err = io.ReadFull(c, make([]byte, 2))
	}
	if err != nil {
		return fmt.Errorf("%s could not send %s %d MB within %s: %w", a.Name, b.Name, n>>20, within, err)
	}
	took := time.Since(start)
	a.l.report("%s -> %s: %d MB in %s, %.0f Mbit/s", a.Name, b.Name, n>>20, took.Round(10*time.Millisecond), float64(n)*8/1e6/took.Seconds())
	return nil
}

type zeroes struct{}

func (zeroes) Read(b []byte) (int, error) { clear(b); return len(b), nil }

// Session is a connection in use, like a terminal: one short message every
// 100 ms from a to b and back.
type Session struct {
	a, b *Agent
	c    net.Conn
	mu   sync.Mutex
	last time.Time // of the last answer
	gap  time.Duration
	gaps int // silences of more than 2 s
	n    int
	err  error
	done chan struct{}
	stop chan struct{}

	quiet atomic.Bool // nothing is sent
}

func open(a, b *Agent) *Session {
	a.l.t.Helper()
	ln := b.Listen(b.MeshIP + ":0")
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		io.Copy(c, c)
		c.Close()
	}()
	s := &Session{a: a, b: b, done: make(chan struct{}), stop: make(chan struct{})}
	e2etest.Within(a.l.t, 60*time.Second, a.Name+" to connect to "+b.Name, func() bool {
		c, err := a.Dial("tcp", ln.Addr().String(), 10*time.Second)
		s.c = c
		return err == nil
	})
	s.last = time.Now()
	go func() {
		for {
			select {
			case <-s.stop:
				s.c.Close()
				return
			case <-time.After(100 * time.Millisecond):
				if !s.quiet.Load() {
					s.c.Write([]byte("12345678"))
				}
			}
		}
	}()
	go func() {
		defer close(s.done)
		var b [8]byte
		for {
			if _, err := io.ReadFull(s.c, b[:]); err != nil {
				if !errors.Is(err, net.ErrClosed) {
					s.err = err
				}
				return
			}
			s.mu.Lock()
			gap := time.Since(s.last)
			s.gap = max(s.gap, gap)
			if gap > 2*time.Second {
				s.gaps++
			}
			s.last = time.Now()
			s.n++
			s.mu.Unlock()
		}
	}()
	return s
}

// openDirect is open on a direct path.
func openDirect(a, b *Agent) *Session {
	a.l.t.Helper()
	tcp(a, b, time.Minute)
	want(a, b, "direct", 2*time.Minute)
	return open(a, b)
}

// answersWithin is how long the next answer takes from now.
func (s *Session) answersWithin(d time.Duration) time.Duration {
	start := time.Now()
	for s.lastAnswer().Before(start) && time.Since(start) < d {
		time.Sleep(20 * time.Millisecond)
	}
	return time.Since(start).Round(100 * time.Millisecond)
}

func (s *Session) lastAnswer() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// close ends the session, reports the longest silence and requires that it was
// shorter than the limit, that the connection did not break and that it answers
// now.
func (s *Session) close(what string, limit time.Duration) {
	s.a.l.t.Helper()
	time.Sleep(time.Second)
	silent := time.Since(s.lastAnswer())
	close(s.stop)
	<-s.done
	gap := max(s.gap, silent)
	s.a.l.report("session during %s: longest silence %s, %d over 2 s, %d answers, error %v", what, gap.Round(10*time.Millisecond), s.gaps, s.n, s.err)
	switch {
	case s.err != nil:
		s.a.l.t.Errorf("the session broke during %s: %v", what, s.err)
	case silent > 5*time.Second:
		s.a.l.t.Errorf("the session is dead after %s: nothing for %s", what, silent.Round(time.Second))
	case gap > limit:
		s.a.l.t.Errorf("the session was silent for %s during %s, want at most %s", gap.Round(100*time.Millisecond), what, limit)
	}
}

// tunnels is how many tunnels the agent has made so far.
func (a *Agent) tunnels() int {
	return strings.Count(a.log.String(), `msg="Handshake message received"`)
}

// roams is how often the agent has changed the path to a peer.
func (a *Agent) roams() int {
	log := a.log.String()
	return strings.Count(log, "Host roamed to") + strings.Count(log, "Host hears us elsewhere")
}

// twoHomes is a machine behind each of two routers, with the kinds of NAT
// given. Both LANs use the same addresses, as they do in practice.
func (l *Lab) twoHomes(natA, natB NAT) (a, b *Agent, ra, rb *Router) {
	l.t.Helper()
	ra = l.router("router-a", inetPrefix+"21", "192.168.1.1", natA, l.inet)
	rb = l.router("router-b", inetPrefix+"22", "192.168.1.1", natB, l.inet)
	agents := l.joinAll(l.host("a", "192.168.1.50", ra.lan), l.host("b", "192.168.1.50", rb.lan))
	return agents[0], agents[1], ra, rb
}

func TestSameLAN(t *testing.T) {
	l := newLab(t)
	r := l.router("router", inetPrefix+"21", "192.168.1.1", Home, l.inet)
	ag := l.joinAll(l.host("a", "192.168.1.50", r.lan), l.host("b", "192.168.1.51", r.lan))
	a, b := ag[0], ag[1]
	tcp(a, b, 60*time.Second)
	want(a, b, "direct 192.168.1.51:", 2*time.Minute)
	want(b, a, "direct 192.168.1.50:", 2*time.Minute)
}

func TestTwoHomes(t *testing.T) {
	l := newLab(t)
	a, b, _, _ := l.twoHomes(Home, Home)
	tcp(a, b, 60*time.Second)
	want(a, b, "direct "+inetPrefix+"22:", 30*time.Second)
	want(b, a, "direct "+inetPrefix+"21:", 30*time.Second)
	bulk(a, b, 20<<20, time.Minute)
}

func TestHomeAndPhone(t *testing.T) {
	l := newLab(t)
	a, b, _, _ := l.twoHomes(Home, Carrier)
	tcp(b, a, 60*time.Second)
	tcp(a, b, 60*time.Second)
	want(a, b, "", 2*time.Minute)
}

// The home router opens a port on request, so the phone reaches it directly.
func TestPortMapping(t *testing.T) {
	l := newLab(t)
	home := l.router("router-a", inetPrefix+"21", "192.168.1.1", Home, l.inet)
	home.natPMP()
	mobile := l.carrier("carrier", inetPrefix+"30")
	ag := l.joinAll(l.host("home", "192.168.1.50", home.lan), l.host("phone", "10.64.0.50", mobile.lan))
	a, b := ag[0], ag[1]
	e2etest.Within(t, 30*time.Second, "the port mapping", func() bool { return strings.Contains(a.log.String(), "portmap: established public mapping") })
	tcp(b, a, 60*time.Second)
	want(b, a, "direct "+inetPrefix+"21:4242", 2*time.Minute)
	want(a, b, "direct "+inetPrefix+"30:", 30*time.Second)
}

// A strict router that opens a port on request: what it sends itself leaves
// through other ports.
func TestPortMappingStrict(t *testing.T) {
	l := newLab(t)
	ra := l.router("router-a", inetPrefix+"21", "192.168.1.1", Home, l.inet)
	rb := l.router("router-b", inetPrefix+"22", "192.168.2.1", Carrier, l.inet)
	rb.natPMP()
	ag := l.joinAll(l.host("a", "192.168.1.50", ra.lan), l.host("b", "192.168.2.50", rb.lan))
	a, b := ag[0], ag[1]
	e2etest.Within(t, 30*time.Second, "the port mapping", func() bool { return strings.Contains(b.log.String(), "portmap: established public mapping") })
	tcp(a, b, 60*time.Second)
	s := open(a, b)
	time.Sleep(2 * time.Minute)
	l.report("a -> b %s, b -> a %s", a.path(b), b.path(a))
	s.close("2 min, strict router with a mapping", 10*time.Second)
}

// Two machines of one home whose router also lets them meet at its outside
// address. They must use the LAN, and stay there.
func TestHairpin(t *testing.T) {
	l := newLab(t)
	r := l.router("router", inetPrefix+"21", "192.168.1.1", Home, l.inet)
	r.hairpin(r.lan.gw)
	r.natPMP()
	ag := l.joinAll(l.host("a", "192.168.1.50", r.lan), l.host("b", "192.168.1.51", r.lan))
	a, b := ag[0], ag[1]
	for _, x := range ag {
		e2etest.Within(t, 30*time.Second, "the port mapping", func() bool { return strings.Contains(x.log.String(), "portmap: established public mapping") })
	}
	tcp(a, b, time.Minute)
	time.Sleep(40 * time.Second) // a heartbeat has told them the mappings
	want(a, b, "direct 192.168.1.51:", 2*time.Minute)
	want(b, a, "direct 192.168.1.50:", 2*time.Minute)
	n := a.roams() + b.roams()
	s := open(a, b)
	time.Sleep(time.Minute)
	s.close("a minute in one home", 2*time.Second)
	if n = a.roams() + b.roams() - n; n > 2 {
		t.Errorf("the path changed %d times in a minute", n)
	}
	l.report("the path changed %d times in a minute", n)
}

// A LAN that works in one direction only: a firewall of one machine drops what
// comes from the other. They have the way through the router, which must not be
// given up for the LAN.
func TestOneWayLAN(t *testing.T) {
	t.Parallel()
	for _, seen := range []string{"192.168.1.1", inetPrefix + "21"} {
		t.Run("router shows up as "+seen, func(t *testing.T) {
			l := newLab(t)
			r := l.router("router", inetPrefix+"21", "192.168.1.1", Home, l.inet)
			r.hairpin(seen)
			r.natPMP()
			ha, hb := l.host("a", "192.168.1.50", r.lan), l.host("b", "192.168.1.51", r.lan)
			ha.sh("nft 'add table ip fw; add chain ip fw in { type filter hook input priority 0; }; add rule ip fw in ip saddr 192.168.1.51 drop'")
			ag := l.joinAll(ha, hb)
			a, b := ag[0], ag[1]
			for _, x := range ag {
				e2etest.Within(t, 30*time.Second, "the port mapping", func() bool { return strings.Contains(x.log.String(), "portmap: established public mapping") })
			}
			meet(a, b)
			time.Sleep(40 * time.Second) // a heartbeat has told them the mappings
			s := open(a, b)
			// Every 1000 packets, and more often than that by runPathWatch, one of
			// them looks for the LAN again.
			time.Sleep(5 * time.Minute)
			l.report("a -> b %s, b -> a %s", a.path(b), b.path(a))
			s.close("5 min on a one-way LAN", time.Second)
			bulk(a, b, 5<<20, 30*time.Second)
			bulk(b, a, 5<<20, 30*time.Second)
		})
	}
}

// The same with two homes: one router drops what comes from the other. They have
// nothing but the relay.
func TestOneWayDirect(t *testing.T) {
	l := newLab(t)
	ra := l.router("router-a", inetPrefix+"21", "192.168.1.1", Home, l.inet)
	rb := l.router("router-b", inetPrefix+"22", "192.168.1.1", Home, l.inet)
	ra.firewall("ip saddr " + inetPrefix + "22 drop")
	ag := l.joinAll(l.host("a", "192.168.1.50", ra.lan), l.host("b", "192.168.1.50", rb.lan))
	a, b := ag[0], ag[1]
	meet(a, b)
	s := open(a, b)
	time.Sleep(5 * time.Minute)
	l.report("a -> b %s, b -> a %s", a.path(b), b.path(a))
	s.close("5 min with a direct path in one direction only", time.Second)
	bulk(a, b, 5<<20, 30*time.Second)
	bulk(b, a, 5<<20, 30*time.Second)
}

// Two machines of one home want the same outside port.
func TestTwoInOneHome(t *testing.T) {
	l := newLab(t)
	ra := l.router("router-a", inetPrefix+"21", "192.168.1.1", Home, l.inet)
	rb := l.router("router-b", inetPrefix+"22", "192.168.1.1", Home, l.inet)
	ag := l.joinAll(l.host("a1", "192.168.1.50", ra.lan), l.host("a2", "192.168.1.51", ra.lan), l.host("b", "192.168.1.50", rb.lan))
	for _, x := range ag {
		for _, y := range ag {
			if x != y {
				tcp(x, y, time.Minute)
				l.report("%s -> %s: %s", x.Name, y.Name, x.path(y))
			}
		}
	}
}

// Two homes of a provider that has all its customers behind one address and
// does not let them meet there. Nothing but the relay is left, also after the
// provider has forgotten about them.
func TestSameProvider(t *testing.T) {
	l := newLab(t)
	isp := l.router("provider", inetPrefix+"40", "100.64.0.1", Carrier, l.inet)
	isp.natTimeout(30)
	isp.firewall("iifname lan oifname lan drop")
	ra := l.router("router-a", "100.64.0.21", "192.168.23.1", Home, isp.lan)
	rb := l.router("router-b", "100.64.0.22", "192.168.24.1", Home, isp.lan)
	ag := l.joinAll(l.host("a", "192.168.23.50", ra.lan), l.host("b", "192.168.24.50", rb.lan))
	a, b := ag[0], ag[1]
	tcp(a, b, time.Minute)
	want(a, b, "relay", time.Minute)
	s := open(a, b)
	time.Sleep(time.Minute)
	s.close("a minute in one provider's network", 2*time.Second)
	time.Sleep(2 * time.Minute)
	if took := tcp(b, a, time.Minute); took > 5*time.Second {
		t.Errorf("the first answer after 2 quiet minutes took %s", took)
	}
	bulk(a, b, 5<<20, time.Minute)
}

// A line that carries smaller packets than the usual 1500 bytes. Whoever sends a
// bigger one is told, or not: many firewalls drop that message.
func TestSmallPackets(t *testing.T) {
	t.Parallel()
	for _, told := range []bool{true, false} {
		t.Run(fmt.Sprintf("told %v", told), func(t *testing.T) {
			l := newLab(t)
			isp := l.router("provider", inetPrefix+"40", "100.64.0.1", Home, l.inet)
			isp.sh("ip link set lan mtu 1400")
			if !told {
				isp.sh("nft 'add table ip quiet; add chain ip quiet out { type filter hook output priority 0; }; add rule ip quiet out icmp type destination-unreachable drop'")
			}
			ra := l.router("router-a", "100.64.0.21", "192.168.1.1", Home, isp.lan)
			ra.sh("ip link set wan mtu 1400")
			rb := l.router("router-b", inetPrefix+"22", "192.168.1.1", Home, l.inet)
			ag := l.joinAll(l.host("a", "192.168.1.50", ra.lan), l.host("b", "192.168.1.50", rb.lan))
			a, b := ag[0], ag[1]
			tcp(a, b, time.Minute)
			want(a, b, "direct", time.Minute)
			bulk(a, b, 5<<20, time.Minute) // a's own router tells
			if told {
				bulk(b, a, 5<<20, time.Minute)
				return
			}
			e2etest.KnownGap(t, "big packets get lost on a line that takes less than 1480 bytes and does not say so", func(g *e2etest.Gap) {
				if err := tryBulk(b, a, 5<<20, 30*time.Second); err != nil {
					g.Errorf("%v", err)
				}
			})
		})
	}
}

// A network that lets nothing out but web pages.
func TestNoUDP(t *testing.T) {
	l := newLab(t)
	ra := l.router("hotel", inetPrefix+"21", "192.168.1.1", Home, l.inet)
	rb := l.router("router-b", inetPrefix+"22", "192.168.1.1", Home, l.inet)
	ra.firewall("iifname lan udp dport != 53 drop")
	ag := l.joinAll(l.host("a", "192.168.1.50", ra.lan), l.host("b", "192.168.1.50", rb.lan))
	e2etest.KnownGap(t, "no way through a network without UDP (a relay on TCP 443)", func(g *e2etest.Gap) {
		if !ag[0].reaches(ag[1], 30*time.Second) {
			g.Errorf("no connection in 30 s")
		}
	})
}

func TestUDPBetweenDevices(t *testing.T) {
	l := newLab(t)
	r := l.router("router", inetPrefix+"21", "192.168.1.1", Home, l.inet)
	ag := l.joinAll(l.host("a", "192.168.1.50", r.lan), l.host("b", "192.168.1.51", r.lan))
	a, b := ag[0], ag[1]
	tcp(a, b, time.Minute)
	pc := b.ListenUDP(b.MeshIP + ":0")
	go func() {
		buf := make([]byte, 64)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			pc.WriteTo(buf[:n], from)
		}
	}()
	c, err := a.Dial("udp4", pc.LocalAddr().String(), time.Second)
	l.must(err)
	defer c.Close()
	e2etest.KnownGap(t, "UDP between devices is blocked by the mesh firewall", func(g *e2etest.Gap) {
		for i := 0; i < 10; i++ {
			c.Write([]byte("ping"))
			c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			if _, err := c.Read(make([]byte, 8)); err == nil {
				return
			}
		}
		g.Errorf("no answer to 10 datagrams")
	})
}

func TestTwoPhones(t *testing.T) {
	l := newLab(t)
	a, b, _, _ := l.twoHomes(Carrier, Carrier)
	tcp(a, b, 60*time.Second)
	want(a, b, "relay", 2*time.Minute)

	// Both LANs use the same address, so whoever looks for a direct path finds
	// itself there and is told that it doesn't know that tunnel.
	n := a.tunnels() + b.tunnels()
	s := open(a, b)
	time.Sleep(2 * time.Minute)
	s.close("2 min on the relay", 2*time.Second)
	bulk(a, b, 5<<20, time.Minute)
	if n = a.tunnels() + b.tunnels() - n; n != 0 {
		t.Errorf("%d new tunnels on a relay that works", n)
	}
}

func TestServerReachesAgents(t *testing.T) {
	l := newLab(t)
	a, b, _, _ := l.twoHomes(Home, Carrier)
	for _, x := range []*Agent{a, b} {
		e2etest.Within(t, 60*time.Second, x.Name+"'s health through the server", func() bool {
			code, _ := l.owner.Do("GET", fmt.Sprintf("/api/networks/%s/nodes/%s/health", l.net.ID, x.NodeID), nil)
			return code == http.StatusOK
		})
	}
}
