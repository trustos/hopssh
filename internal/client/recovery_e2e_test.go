package client

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	t.Cleanup(func() { _ = m.c.Stop() })
	return m
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
	tbs := &cert.TBSCertificate{Version: cert.Version1, Name: nodeID, PublicKey: pub, Curve: cert.Curve_CURVE25519,
		Networks: []netip.Prefix{m.addrs[nodeID]}, NotBefore: nb, NotAfter: nb.Add(24 * time.Hour)}
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

func freeUDPPort(t *testing.T) int {
	t.Helper()
	p, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	return p.LocalAddr().(*net.UDPAddr).Port
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
	if err := m.c.Connect(context.Background(), "home"); err != nil || !m.live("home") {
		t.Errorf("reconnect: err %v, live %v", err, m.live("home"))
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
		"watchNetworkChanges", "runWatcherWatchdog", "runPathQuality", "runMeshKeepalive"}
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
