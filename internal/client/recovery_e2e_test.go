package client

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	mathrand "math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slackhq/nebula/cert"
	"golang.org/x/crypto/curve25519"

	"github.com/trustos/hopssh/internal/nebulacfg"
)

// End-to-end tests: a real Client with real userspace Nebula against a fake
// control plane. Each network takes ~6s to start (tunnel warm-up), so these
// are skipped with -short.

// Set once for the whole package, not per test: instance goroutines outlive
// the test that started them.
func init() {
	nebulacfg.PortmapEnabled = false // don't ask the router for a port mapping
	configDir = filepath.Join(os.TempDir(), "hopssh-test-no-such-dir")
}

const e2eSubnet = "198.18.77." // benchmarking range: never a real mesh

type e2eMesh struct {
	t       *testing.T
	ca      cert.Certificate
	priv    ed25519.PrivateKey
	c       *Client
	dir     string
	server  *httptest.Server
	onRenew atomic.Pointer[func(nodeID string) int] // HTTP status to answer with
	addrs   map[string]netip.Prefix                 // by node ID
}

func newE2EMesh(t *testing.T) *e2eMesh {
	t.Helper()
	if testing.Short() {
		t.Skip("real Nebula")
	}
	// Every peer in these tests lives on a loopback address, which only the
	// userspace netstack serves; the agent would upgrade to a real TUN device
	// because the test process is root. Pin what enroll() wrote (see
	// tunUpgradeAllowed).
	prev := tunUpgradeAllowed
	tunUpgradeAllowed = func() bool { return false }
	t.Cleanup(func() { tunUpgradeAllowed = prev })
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tbs := &cert.TBSCertificate{Version: cert.Version1, Name: "ca", IsCA: true, PublicKey: pub, Curve: cert.Curve_CURVE25519,
		NotBefore: now.Add(-365 * 24 * time.Hour), NotAfter: now.Add(365 * 24 * time.Hour)}
	ca, err := tbs.Sign(nil, cert.Curve_CURVE25519, priv)
	if err != nil {
		t.Fatal(err)
	}
	m := &e2eMesh{t: t, ca: ca, priv: priv, addrs: map[string]netip.Prefix{}}
	m.server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.server.Close)

	m.c, m.dir = newTestClientForBootResilience(t)
	m.c.recoveryBackoff = []time.Duration{200 * time.Millisecond}
	// Stop cancels the renewal loop, but a POST already on its way still
	// writes node.crt and node.key, and a reload still running starts and
	// stops a Nebula. t.TempDir removes m.dir when the test ends, and it
	// fails with "directory not empty" if that write lands in the moment
	// between. Cleanups run in reverse order, so this runs before that
	// removal; it stops the client itself first, because waiting for a loop
	// that nothing has cancelled would just burn the timeout.
	t.Cleanup(func() {
		_ = m.c.Stop()
		stacks := ""
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			buf := make([]byte, 8<<20)
			stacks = string(buf[:runtime.Stack(buf, true)])
			if !strings.Contains(stacks, "/internal/client.runCertRenewal(") {
				return
			}
		}
		m.t.Errorf("a renewal loop is still running 30s after Stop:\n%s", goroutineStackFor(stacks, "/internal/client.runCertRenewal("))
	})
	return m
}

// goroutineStackFor returns the stack of each goroutine whose text contains
// want. Test-only: it answers "where is the loop that Stop did not stop?"
// without a debugger.
func goroutineStackFor(stacks, want string) string {
	var out []string
	for _, g := range strings.Split(stacks, "\ngoroutine ") {
		if strings.Contains(g, want) {
			out = append(out, "goroutine "+g)
		}
	}
	return strings.Join(out, "\n\n")
}

func (m *e2eMesh) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/renew" {
		w.Write([]byte("{}"))
		return
	}
	var req struct{ NodeID string }
	json.NewDecoder(r.Body).Decode(&req)
	if fn := m.onRenew.Load(); fn != nil {
		if status := (*fn)(req.NodeID); status != http.StatusOK {
			http.Error(w, "test", status)
			return
		}
	}
	crt, key := m.issue(req.NodeID, 0)
	json.NewEncoder(w).Encode(map[string]string{"nodeCert": crt, "nodeKey": key})
}

// issue returns a 24h cert of the given age.
func (m *e2eMesh) issue(nodeID string, age time.Duration) (crt, key string) {
	k := make([]byte, 32)
	rand.Read(k)
	pub, err := curve25519.X25519(k, curve25519.Basepoint)
	if err != nil {
		panic(err)
	}
	nb := time.Now().Add(-time.Minute - age)
	// Group "node", as every cert the server issues has: the firewall blocks
	// of enrollments allow by it.
	tbs := &cert.TBSCertificate{Version: cert.Version1, Name: nodeID, PublicKey: pub, Curve: cert.Curve_CURVE25519,
		Networks: []netip.Prefix{m.addrs[nodeID]}, Groups: []string{"node"}, NotBefore: nb, NotAfter: nb.Add(24 * time.Hour)}
	c, err := tbs.Sign(m.ca, cert.Curve_CURVE25519, m.priv)
	if err != nil {
		panic(err)
	}
	p, err := c.MarshalPEM()
	if err != nil {
		panic(err)
	}
	return string(p), string(cert.MarshalPrivateKeyToPEM(cert.Curve_CURVE25519, k))
}

// Test ports come from a band the OS never hands out, and no port is handed
// out twice in one run. Asking the OS gives the ephemeral range, and Nebula
// binds with SO_REUSEPORT on Linux, so two instances can hold one port at the
// same time: the second one starts fine, and a test that then asks whether the
// port is free after a close sees the other instance's socket and fails at
// random. That is what TestUserspaceClose_DoesNotExit and
// TestE2E_DisconnectDuringRenewal did on CI, on ports in the ephemeral range.
const (
	udpTestPortBase = 20000
	udpTestPortSpan = 10000
)

var (
	udpPortMu      sync.Mutex
	udpPortsHanded = map[int]bool{}
)

// reserveUDPPorts returns the first of n free UDP ports in a row and marks them
// taken for the rest of this test binary.
func reserveUDPPorts(t *testing.T, n int) int {
	t.Helper()
	udpPortMu.Lock()
	defer udpPortMu.Unlock()
	for i := 0; i < 100; i++ {
		base := udpTestPortBase + mathrand.Intn(udpTestPortSpan-n)
		used := false
		for p := base; p < base+n; p++ {
			if udpPortsHanded[p] || !udpPortAvailable(p) {
				used = true
				break
			}
		}
		if used {
			continue
		}
		for p := base; p < base+n; p++ {
			udpPortsHanded[p] = true
		}
		return base
	}
	t.Fatalf("no %d free UDP ports in a row", n)
	return 0
}

func freeUDPPort(t *testing.T) int { return reserveUDPPorts(t, 1) }

// The OS is not asked for a port, so it cannot hand out one that another
// instance already holds.
func TestFreeUDPPort_NotFromTheEphemeralRange(t *testing.T) {
	a, b := freeUDPPort(t), freeUDPPort(t)
	if a == b {
		t.Fatalf("one port handed out twice: %d", a)
	}
	for _, p := range []int{a, b} {
		if p < udpTestPortBase || p >= udpTestPortBase+udpTestPortSpan {
			t.Fatalf("port %d is not in the band the OS never hands out", p)
		}
	}
}

func udpPortHeld(port int) bool {
	p, err := net.ListenPacket("udp4", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return true
	}
	p.Close()
	return false
}

func localUDP(port int) string { return fmt.Sprintf("127.0.0.1:%d", port) }

// enroll adds a userspace enrollment whose cert has the given age. staticHosts
// maps a peer's mesh IP to its underlay addresses.
func (m *e2eMesh) enroll(name, ip string, port int, certAge time.Duration, staticHosts map[string][]string) {
	m.t.Helper()
	m.addrs["node-"+name] = netip.MustParsePrefix(ip + "/24")
	dir := filepath.Join(m.dir, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		m.t.Fatal(err)
	}
	crt, key := m.issue("node-"+name, certAge)
	caPEM, err := m.ca.MarshalPEM()
	if err != nil {
		m.t.Fatal(err)
	}
	hosts := fmt.Sprintf("  %q: [\"127.0.0.1:9\"]\n", e2eSubnet+"1")
	for peer, addrs := range staticHosts {
		hosts += fmt.Sprintf("  %q: [\"%s\"]\n", peer, strings.Join(addrs, `", "`))
	}
	yaml := fmt.Sprintf(`pki: {ca: %[1]s/ca.crt, cert: %[1]s/node.crt, key: %[1]s/node.key}
static_host_map:
%[2]slighthouse: {am_lighthouse: false, hosts: [%[3]q]}
listen: {host: 0.0.0.0, port: %[4]d}
tun: {user: true}
firewall:
  outbound: [{port: any, proto: any, host: any}]
  inbound: [{port: any, proto: any, host: any}]
`, dir, hosts, e2eSubnet+"1", port)
	for file, content := range map[string]string{
		"ca.crt": string(caPEM), "node.crt": crt, "node.key": key, "token": "tok",
		"node-id": "node-" + name, "endpoint": m.server.URL, "tun-mode": "userspace", "nebula.yaml": yaml,
	} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600); err != nil {
			m.t.Fatal(err)
		}
	}
	if err := m.c.enrolls.Add(&Enrollment{Name: name, NodeID: "node-" + name, Endpoint: m.server.URL, TunMode: "userspace", ListenPort: port}); err != nil {
		m.t.Fatal(err)
	}
}

func (m *e2eMesh) live(name string) bool {
	inst := m.c.instances.get(name)
	return inst != nil && inst.control() != nil
}

func (m *e2eMesh) certValid(name string) bool {
	problem, _ := certStartProblem(filepath.Join(m.dir, name), time.Now())
	return problem == ""
}

// within is eventually with room for tunnel warm-up.
func within(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestE2E_RootStaysInUserspace is the tripwire for the pin in newE2EMesh. Run
// as root (CI runs the package as root, and the kernel-TUN test must be root),
// an unpinned harness starts a real TUN device instead of the userspace one its
// 127.0.0.1 peers need: on Linux five path tests failed as root and passed as
// an ordinary user, which is how this was found (docs/wiki/log.md, 2026-11-27).
// As an ordinary user it passes either way; it is here for the root run.
func TestE2E_RootStaysInUserspace(t *testing.T) {
	m := newE2EMesh(t)
	m.enroll("a", e2eSubnet+"6", freeUDPPort(t), 0, nil)
	if err := m.c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	inst := m.c.instances.get("a")
	if inst == nil {
		t.Fatal("no live instance")
	}
	if mode := currentTunMode(inst); mode != "userspace" {
		t.Errorf("tun mode %q at uid %d: enroll() wrote userspace, so this test would drive a data plane its peers on 127.0.0.1 cannot reach", mode, os.Getuid())
	}
}

// TestE2E_LongOffMachineRecovers is the 2026-11 MacBook: two networks, both
// certs expired half a year ago, the control plane refusing renewals at boot.
// On v0.11.26 both stay dead across boot, Connect and reboot.
func TestE2E_LongOffMachineRecovers(t *testing.T) {
	m := newE2EMesh(t)
	homePort, workPort := freeUDPPort(t), freeUDPPort(t)
	m.enroll("home", e2eSubnet+"6", homePort, 180*24*time.Hour, nil)
	m.enroll("work", e2eSubnet+"7", workPort, 180*24*time.Hour, nil)
	var homePosts, workPosts atomic.Int64
	refuse := func(nodeID string) int {
		posts, status := &homePosts, http.StatusTooManyRequests
		if nodeID == "node-work" {
			posts, status = &workPosts, http.StatusBadGateway
		}
		if posts.Add(1) <= 3 {
			return status
		}
		return http.StatusOK
	}
	m.onRenew.Store(&refuse)

	if err := m.c.Start(context.Background()); err == nil {
		t.Error("Start: want an error while every network is refused")
	}
	if udpPortHeld(homePort) || udpPortHeld(workPort) {
		t.Fatal("ORPHAN: a Nebula holds the port although no network is up")
	}
	within(t, 40*time.Second, "both networks up", func() bool { return m.live("home") && m.live("work") })
	if !m.certValid("home") || !m.certValid("work") {
		t.Error("certs on disk are still not valid")
	}
	for _, name := range []string{"home", "work"} {
		if _, ok := m.c.recoveryInfo(name); ok {
			within(t, 20*time.Second, name+"'s recovery loop to end", func() bool { _, ok := m.c.recoveryInfo(name); return !ok })
		}
	}

	// And the user can still turn one off for good.
	if err := m.c.Disconnect(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	within(t, 5*time.Second, "work's UDP port to be released", func() bool { return !udpPortHeld(workPort) })
	time.Sleep(time.Second)
	if m.live("work") || udpPortHeld(workPort) {
		t.Error("work came back after Disconnect")
	}
	if !m.live("home") {
		t.Error("disconnecting work took home down")
	}
}

// TestE2E_DisconnectDuringRenewal: the renewal is answered only after the
// user disconnected. On v0.11.26 it then starts an orphan Nebula that holds
// the UDP port until the process ends, and every reconnect fails.
func TestE2E_DisconnectDuringRenewal(t *testing.T) {
	m := newE2EMesh(t)
	port := freeUDPPort(t)
	m.enroll("home", e2eSubnet+"6", port, 15*time.Hour, nil)
	entered, release := make(chan struct{}, 8), make(chan struct{})
	block := func(string) int {
		entered <- struct{}{}
		<-release
		return http.StatusOK
	}
	m.onRenew.Store(&block)

	if err := m.c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no renewal although the cert is past 60% of its life")
	}
	if err := m.c.Disconnect(context.Background(), "home"); err != nil {
		t.Fatal(err)
	}
	close(release)
	within(t, 5*time.Second, "the UDP port to be released", func() bool { return !udpPortHeld(port) })
	time.Sleep(2 * time.Second)
	if m.live("home") || udpPortHeld(port) {
		t.Fatal("ORPHAN: the renewal brought a disconnected network back")
	}

	m.onRenew.Store(nil)
	if err := m.c.Connect(context.Background(), "home"); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	// Connect returns with the instance up, not merely scheduled: it started
	// Nebula itself before returning. A glance at live() passes 10 of 10 on
	// the Mac, and failed once on CI (ubuntu-latest, -race, 2026-11-27 00:36)
	// — err <nil>, live() false 100 ms later. That one is not reproduced in
	// 15 Linux runs and 8 under -race here, so its cause is not known. The
	// only mechanism found that could do it: a reload closes the running
	// Nebula before starting the new one (the v0.10.33 ordering, pinned by
	// TestReloadNebula_I1), which leaves control() nil for the length of the
	// swap — up to 2 s of port-free wait plus the start. Nothing here fires a
	// renewal that soon (the loop's first check is one 60 s tick in), so that
	// stays a guess. Wait out a swap of that size, then check it stays up:
	// the stay is the part that matters — a reconnect that never comes back,
	// or comes back and dies, still fails here.
	within(t, 5*time.Second, "the reconnected network to be live", func() bool { return m.live("home") })
	time.Sleep(3 * time.Second)
	if !m.live("home") {
		t.Error("the reconnected network went dead again 3s after it came up")
	}
}

// TestE2E_KeepaliveOnUserspaceNetwork: two "in-app" networks on one machine
// reach each other, and a watchdog restart brings one back.
func TestE2E_KeepaliveOnUserspaceNetwork(t *testing.T) {
	m := newE2EMesh(t)
	portA, portB := freeUDPPort(t), freeUDPPort(t)
	ipA, ipB := e2eSubnet+"6", e2eSubnet+"7"
	m.enroll("a", ipA, portA, time.Hour, map[string][]string{ipB: {localUDP(portB)}})
	m.enroll("b", ipB, portB, time.Hour, map[string][]string{ipA: {localUDP(portA)}})
	if err := m.c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	reachable := func() bool { // also triggers the handshake
		return keepaliveProbe(m.c.instances.get("a"), fmt.Sprintf("%s:%d", ipB, agentAPIPort)) == nil
	}
	within(t, 20*time.Second, "a to reach b over the mesh", reachable)
	probed, succeeded, _, peers := keepaliveOneCycle(m.c.instances.get("a"))
	if peers != 1 || probed != 1 || succeeded != 1 {
		t.Errorf("keepalive: peers %d, probed %d, succeeded %d; want 1, 1, 1", peers, probed, succeeded)
	}

	stale := m.c.instances.get("a")
	ln, err := m.c.instances.get("b").currentSvc().Listen("tcp", ":7777")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			if _, err := ln.Accept(); err != nil {
				return
			}
		}
	}()
	conn, err := stale.currentSvc().(meshDialer).DialContext(context.Background(), "tcp", ipB+":7777")
	if err != nil {
		t.Fatal(err)
	}
	unblocked := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 1))
		unblocked <- err
	}()

	if err := stale.restartFn(); err != nil {
		t.Fatalf("watchdog restart: %v", err)
	}
	select {
	case err := <-unblocked:
		if err == nil {
			t.Error("a connection on the closed network returned data")
		}
	case <-time.After(5 * time.Second):
		t.Error("a read on the closed network's connection is still blocked")
	}
	if m.c.instances.get("a") == stale || !m.live("a") {
		t.Error("the watchdog restart did not replace the instance")
	}
	if !m.c.instances.get("a").restartCoolingDown() {
		t.Error("the replacement must not be restartable right away")
	}
	within(t, 20*time.Second, "a to reach b after the restart", reachable)
}

// TestE2E_RefusedAddressFirst: a peer's first address is one the kernel
// refuses to send to, as a LAN address is once the peer has left that LAN. The
// handshake goes to all addresses in one batch and must still reach the good
// one (vendor patch 28; macOS used to drop everything behind the bad one).
func TestE2E_RefusedAddressFirst(t *testing.T) {
	m := newE2EMesh(t)
	portA, portB := freeUDPPort(t), freeUDPPort(t)
	ipA, ipB := e2eSubnet+"6", e2eSubnet+"7"
	m.enroll("a", ipA, portA, time.Hour, map[string][]string{ipB: {localUDP(0), localUDP(portB)}})
	m.enroll("b", ipB, portB, time.Hour, nil)
	if err := m.c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	within(t, 20*time.Second, "a to reach b", func() bool {
		return keepaliveProbe(m.c.instances.get("a"), fmt.Sprintf("%s:%d", ipB, agentAPIPort)) == nil
	})
}

// TestE2E_BackgroundWork: a network that is up has every loop that keeps it up
// exactly once, also after a renewal and after a watchdog restart, and one that
// is turned off has none of them.
func TestE2E_BackgroundWork(t *testing.T) {
	m := newE2EMesh(t)
	m.enroll("home", e2eSubnet+"6", freeUDPPort(t), time.Hour, nil)
	loops := []string{"runCertRenewal", "runRenewalWatchdog", "runHeartbeat",
		"watchNetworkChanges", "runWatcherWatchdog", "runPathQuality", "runMeshKeepalive", "runPathWatch", "runSendWatch"}
	expect := func(n int, when string) {
		t.Helper()
		var wrong []string
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			buf := make([]byte, 8<<20)
			stacks := string(buf[:runtime.Stack(buf, true)])
			wrong = nil
			for _, loop := range loops {
				if got := strings.Count(stacks, "/internal/client."+loop+"("); got != n {
					wrong = append(wrong, fmt.Sprintf("%s ×%d", loop, got))
				}
			}
			if wrong == nil {
				return
			}
		}
		t.Fatalf("%s: %s; want ×%d", when, strings.Join(wrong, ", "), n)
	}

	expect(0, "before the start")
	if err := m.c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	expect(1, "up")
	if err := m.c.ForceRenew(context.Background(), "home"); err != nil {
		t.Fatal(err)
	}
	expect(1, "after a renewal")
	stale := m.c.instances.get("home")
	if err := stale.restartFn(); err != nil {
		t.Fatal(err)
	}
	if m.c.instances.get("home") == stale {
		t.Error("the watchdog restart did not replace the instance")
	}
	expect(1, "after a watchdog restart")
	if err := m.c.Disconnect(context.Background(), "home"); err != nil {
		t.Fatal(err)
	}
	expect(0, "turned off")
}
