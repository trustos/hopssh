package netlab

import (
	"syscall"
	"testing"
	"time"
)

// carrier is a mobile network.
func (l *Lab) carrier(name, wan string) *Router {
	l.t.Helper()
	return l.router(name, wan, "10.64.0.1", Carrier, l.inet)
}

func TestRoaming(t *testing.T) {
	l := newLab(t)
	a, b, ra, _ := l.twoHomes(Home, Home)
	mobile := l.carrier("carrier", inetPrefix+"30")
	s := openDirect(a, b)
	time.Sleep(5 * time.Second)

	a.unplug("eth0")
	time.Sleep(2 * time.Second) // until the hotspot is on
	a.plug("eth0", "10.64.0.50", mobile.lan)
	time.Sleep(60 * time.Second)
	l.report("on mobile: %s", a.path(b))
	s.close("home -> mobile", 20*time.Second)

	s = open(a, b)
	a.unplug("eth0")
	time.Sleep(2 * time.Second)
	a.plug("eth0", "192.168.1.50", ra.lan)
	time.Sleep(60 * time.Second)
	l.report("home again: %s", a.path(b))
	s.close("mobile -> home", 20*time.Second)
}

// Another network that happens to give out the same address: two hotspots, two
// homes with the router's defaults.
func TestRoamingSameAddress(t *testing.T) {
	l := newLab(t)
	a, b, _, _ := l.twoHomes(Home, Home)
	other := l.router("router-c", inetPrefix+"23", "192.168.1.1", Home, l.inet)
	s := openDirect(a, b)
	time.Sleep(5 * time.Second)
	a.unplug("eth0")
	a.plug("eth0", "192.168.1.50", other.lan)
	time.Sleep(90 * time.Second)
	want(a, b, "direct", time.Second)
	s.close("home -> other home, same address", 20*time.Second)
}

func TestNewPublicAddress(t *testing.T) {
	l := newLab(t)
	a, b, ra, _ := l.twoHomes(Home, Home)
	s := openDirect(a, b)
	time.Sleep(5 * time.Second)
	ra.newAddress(inetPrefix + "24")
	time.Sleep(90 * time.Second)
	want(a, b, "direct", time.Second)
	s.close("new public address", 20*time.Second)
}

func TestRouterReboot(t *testing.T) {
	l := newLab(t)
	a, b, ra, _ := l.twoHomes(Home, Home)
	s := openDirect(a, b)
	time.Sleep(5 * time.Second)
	ra.line.cut.Store(true)
	time.Sleep(30 * time.Second)
	ra.forget()
	ra.line.cut.Store(false)
	time.Sleep(60 * time.Second)
	want(a, b, "direct", time.Second)
	s.close("30 s without the router", 45*time.Second)
}

func TestServerRestart(t *testing.T) {
	t.Parallel()
	for _, how := range []string{"stopped", "killed"} {
		t.Run(how, func(t *testing.T) {
			l := newLab(t)
			a, b, _, _ := l.twoHomes(Home, Home)
			c := l.join(l.host("c", "10.64.0.50", l.carrier("carrier", inetPrefix+"30").lan))
			direct, relayed := openDirect(a, b), open(a, c)
			time.Sleep(5 * time.Second)
			l.report("before: a-b %s, a-c %s", a.path(b), a.path(c))
			if how == "killed" {
				l.server.kill()
			} else {
				l.server.stop()
			}
			time.Sleep(10 * time.Second)
			l.server.run()
			l.report("relayed: answer %s after the server is back", relayed.answersWithin(3*time.Minute))
			l.report("after: a-b %s, a-c %s", a.path(b), a.path(c))
			direct.close("server restart, direct", 2*time.Second)
			relayed.close("server restart, relayed", 30*time.Second)
		})
	}
}

// A direct path in use stops working in one direction.
func TestOneDirectionBreaks(t *testing.T) {
	t.Parallel()
	for _, deaf := range []string{"a", "b"} {
		t.Run(deaf+" hears nothing", func(t *testing.T) {
			l := newLab(t)
			a, b, ra, rb := l.twoHomes(Home, Home)
			s := openDirect(a, b)
			time.Sleep(5 * time.Second)
			if deaf == "a" {
				ra.firewall("ip saddr " + inetPrefix + "22 drop")
			} else {
				rb.firewall("ip saddr " + inetPrefix + "21 drop")
			}
			time.Sleep(90 * time.Second)
			l.report("a -> b %s, b -> a %s", a.path(b), b.path(a))
			s.close("a direct path that breaks in one direction", 20*time.Second)
			bulk(a, b, 5<<20, 30*time.Second)
			bulk(b, a, 5<<20, 30*time.Second)
		})
	}
}

// Somebody on the other end of a relayed connection walks to another network.
func TestRelayedPeerMoves(t *testing.T) {
	l := newLab(t)
	home := l.router("router-a", inetPrefix+"21", "192.168.1.1", Home, l.inet)
	one, two := l.carrier("carrier-1", inetPrefix+"30"), l.router("carrier-2", inetPrefix+"31", "10.65.0.1", Carrier, l.inet)
	ag := l.joinAll(l.host("home", "192.168.1.50", home.lan), l.host("phone", "10.64.0.50", one.lan))
	a, b := ag[0], ag[1]
	tcp(a, b, time.Minute)
	s := open(a, b)
	time.Sleep(5 * time.Second)
	b.unplug("eth0")
	b.plug("eth0", "10.65.0.50", two.lan)
	time.Sleep(60 * time.Second)
	l.report("after: %s", a.path(b))
	s.close("the other end moves", 20*time.Second)
}

// Two machines of one LAN, one leaves the house and comes back.
func TestLeaveTheLAN(t *testing.T) {
	l := newLab(t)
	home := l.router("router", inetPrefix+"21", "192.168.1.1", Home, l.inet)
	home.hairpin(home.lan.gw)
	home.natPMP()
	mobile := l.carrier("carrier", inetPrefix+"30")
	ag := l.joinAll(l.host("a", "192.168.1.50", home.lan), l.host("b", "192.168.1.51", home.lan))
	a, b := ag[0], ag[1]
	tcp(a, b, time.Minute)
	want(a, b, "direct 192.168.1.51:", 2*time.Minute)
	s := open(a, b)
	time.Sleep(5 * time.Second)
	a.unplug("eth0")
	a.plug("eth0", "10.64.0.50", mobile.lan)
	time.Sleep(60 * time.Second)
	l.report("outside: %s", a.path(b))
	a.unplug("eth0")
	a.plug("eth0", "192.168.1.50", home.lan)
	want(a, b, "direct 192.168.1.51:", 30*time.Second)
	want(b, a, "direct 192.168.1.50:", 30*time.Second)
	time.Sleep(20 * time.Second) // by now a has noticed the other network and has made new tunnels
	want(a, b, "direct 192.168.1.51:", time.Second)
	want(b, a, "direct 192.168.1.50:", time.Second)
	s.close("out of the house and back", 20*time.Second)
}

// The server is away and an agent starts: it has to find the others by itself.
func TestAgentStartsWithoutServer(t *testing.T) {
	l := newLab(t)
	a, b, _, _ := l.twoHomes(Home, Home)
	tcp(a, b, time.Minute)
	time.Sleep(70 * time.Second) // a heartbeat, which writes down where the others are
	l.server.stop()
	a.stop()
	a.run()
	tcp(a, b, 2*time.Minute)
}

// A laptop sleeps, and so do its programs: nothing is on its way when it falls
// asleep, and what is sent to it in the meantime is lost.
func TestSleep(t *testing.T) {
	l := newLab(t)
	a, b, ra, _ := l.twoHomes(Home, Home)
	s := openDirect(a, b)
	time.Sleep(5 * time.Second)
	s.quiet.Store(true)
	time.Sleep(time.Second)
	a.signal(syscall.SIGSTOP)
	a.sh("ip link set eth0 down")
	time.Sleep(60 * time.Second)
	ra.forget()
	a.sh("ip link set eth0 up; ip route replace default via " + ra.lan.gw)
	a.signal(syscall.SIGCONT)
	s.quiet.Store(false)
	took := s.answersWithin(2 * time.Minute)
	l.report("answer %s after waking up, path %s", took, a.path(b))
	if took > 10*time.Second {
		t.Errorf("no answer until %s after waking up", took)
	}
	s.close("60 s of sleep", 80*time.Second)
}

// Quiet for longer than any router remembers a connection.
func TestIdle(t *testing.T) {
	l := newLab(t)
	a, b, ra, rb := l.twoHomes(Home, Home)
	ra.natTimeout(30)
	rb.natTimeout(30)
	tcp(a, b, time.Minute)
	want(a, b, "direct", 2*time.Minute)
	for _, quiet := range []time.Duration{45 * time.Second, 5 * time.Minute} {
		time.Sleep(quiet)
		l.report("quiet for %s: %s", quiet, a.path(b))
		tcp(a, b, time.Minute)
	}
}

func TestBadLine(t *testing.T) {
	l := newLab(t)
	a, b, ra, rb := l.twoHomes(Home, Home)
	bad := Wire{Delay: 40 * time.Millisecond, Jitter: 20 * time.Millisecond, Loss: 0.05}
	ra.line.set(bad)
	rb.line.set(bad)
	s := openDirect(a, b)
	time.Sleep(2 * time.Minute)
	l.report("path %s", a.path(b))
	s.close("2 min with 5 % loss each way", 5*time.Second)
}

// A direct path becomes possible only later, as when a laptop leaves a strict
// network: the connection must not stay on the relay.
func TestRelayBecomesDirect(t *testing.T) {
	l := newLab(t)
	a, b, ra, _ := l.twoHomes(Home, Home)
	ra.firewall(`ip daddr ` + inetPrefix + `22 udp dport 4242 drop`)
	a.stop()
	a.run()
	tcp(a, b, time.Minute)
	want(a, b, "relay", 30*time.Second)
	ra.sh("nft flush chain ip filter forward; nft add rule ip filter forward ct state established,related accept; nft add rule ip filter forward iifname wan drop")
	want(a, b, "direct", 2*time.Minute)
}

// A certificate is renewed while somebody is working.
func TestRenewalDuringSession(t *testing.T) {
	l := newLab(t)
	a, b, _, _ := l.twoHomes(Home, Home)
	s := openDirect(a, b)
	time.Sleep(5 * time.Second)
	l.must(a.local("POST", "/local/renew", map[string]string{"enrollment": labName}, nil))
	time.Sleep(60 * time.Second)
	l.report("after: %s", a.path(b))
	s.close("renewal", 2*time.Second)
}
