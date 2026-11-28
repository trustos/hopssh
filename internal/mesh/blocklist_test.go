package mesh

import (
	"fmt"
	"io"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slackhq/nebula"
	"github.com/slackhq/nebula/config"
	"github.com/slackhq/nebula/logging"
	"github.com/slackhq/nebula/overlay"
	"github.com/slackhq/nebula/service"

	"github.com/trustos/hopssh/internal/crypto"
	"github.com/trustos/hopssh/internal/db"
	"github.com/trustos/hopssh/internal/nebulacfg"
	"github.com/trustos/hopssh/internal/pki"
)

// TestLighthouseRefusesDeletedNodes: the lighthouse refuses the certificate
// of a deleted node, from its start and from the moment the node is deleted,
// with real Nebula on both sides; once the entry expires it lets it in again.
// Without it a deleted device kept asking the lighthouse for its peers'
// addresses and kept being relayed for the day its certificate was valid.
func TestLighthouseRefusesDeletedNodes(t *testing.T) {
	if testing.Short() {
		t.Skip("real Nebula")
	}
	pair, err := db.Open(filepath.Join(t.TempDir(), "hopssh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pair.Close() })
	if err := db.Migrate(pair.WriteDB); err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEncryptor(strings.Repeat("00", 32))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pair.WriteDB.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	networks, nodes := db.NewNetworkStore(pair, enc), db.NewNodeStore(pair, enc)
	exec(`INSERT INTO users (id, email, name, password_hash) VALUES ('owner', 'owner@test', 'owner', 'x')`)

	const netID = "net-0000-0001"
	port := freePort(t)
	lhPort := int64(port)
	subnet, err := networks.AllocateSubnet()
	if err != nil {
		t.Fatal(err)
	}
	ca, err := pki.GenerateCA("net", 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	lhIP, err := pki.ServerAddress(subnet)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := pki.IssueCert(ca.CertPEM, ca.KeyPEM, "hopssh-server", lhIP, []string{"admin", "node"}, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := networks.Create(&db.Network{ID: netID, UserID: "owner", Name: "net", Slug: "net", NebulaSubnet: subnet,
		NebulaCACert: ca.CertPEM, NebulaCAKey: ca.KeyPEM, ServerCert: srv.CertPEM, ServerKey: srv.KeyPEM, LighthousePort: &lhPort}); err != nil {
		t.Fatal(err)
	}
	type node struct {
		ip        netip.Prefix
		cert, key []byte
	}
	enroll := func(id string, index int) node {
		t.Helper()
		ip, err := pki.NodeAddress(subnet, index)
		if err != nil {
			t.Fatal(err)
		}
		c, err := pki.IssueCert(ca.CertPEM, ca.KeyPEM, id, ip, []string{"node"}, 24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if err := nodes.Create(&db.Node{ID: id, NetworkID: netID, Hostname: id, AgentToken: "x", Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := nodes.CompleteEnrollment(id, c.CertPEM, c.KeyPEM, id, "linux", "arm64"); err != nil {
			t.Fatal(err)
		}
		return node{ip, c.CertPEM, c.KeyPEM}
	}
	a, b := enroll("a", 0), enroll("b", 1)
	if err := nodes.Delete("b"); err != nil {
		t.Fatal(err)
	}

	nm, err := NewNetworkManager(networks, nodes, db.NewDNSRecordStore(pair))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nm.Stop)
	inst, err := nm.GetInstance(netID)
	if err != nil {
		t.Fatal(err)
	}

	// join runs a device with n's certificate that uses the lighthouse.
	join := func(n node) *service.Service {
		t.Helper()
		text := fmt.Sprintf(`
tun:
  user: true
cipher: %s
pki:
  ca: |
%s
  cert: |
%s
  key: |
%s
static_host_map:
  %q: ["127.0.0.1:%d"]
lighthouse:
  hosts: [%q]
  interval: 1
listen:
  host: 127.0.0.1
  port: 0
handshakes:
  try_interval: 100ms
firewall:
  outbound: [{port: any, proto: any, host: any}]
  inbound: [{port: any, proto: any, host: any}]
`, nebulacfg.Cipher, indentPEM(string(ca.CertPEM), 4), indentPEM(string(n.cert), 4), indentPEM(string(n.key), 4),
			lhIP.Addr().String(), port, lhIP.Addr().String())
		var cfg config.C
		if err := cfg.LoadString(text); err != nil {
			t.Fatal(err)
		}
		ctrl, err := nebula.Main(&cfg, false, "device", logging.NewLogger(io.Discard), overlay.NewUserDeviceFromConfig)
		if err != nil {
			t.Fatal(err)
		}
		svc, err := service.New(ctrl)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { svc.Close() })
		return svc
	}
	// tunnel reports whether the lighthouse has a tunnel with n.
	tunnel := func(n node) bool {
		return inst.ctrl.GetHostInfoByVpnAddr(n.ip.Addr(), false) != nil
	}
	within := func(d time.Duration, what string, ok func() bool) time.Duration {
		t.Helper()
		start := time.Now()
		for time.Since(start) < d {
			if ok() {
				return time.Since(start)
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("%s: not within %v", what, d)
		return 0
	}
	never := func(d time.Duration, what string, bad func() bool) {
		t.Helper()
		for start := time.Now(); time.Since(start) < d; time.Sleep(50 * time.Millisecond) {
			if bad() {
				t.Fatalf("%s, after %v", what, time.Since(start).Round(time.Millisecond))
			}
		}
	}

	join(a)
	within(10*time.Second, "a, which exists, gets a tunnel with the lighthouse", func() bool { return tunnel(a) })
	join(b)
	never(5*time.Second, "b was deleted before the lighthouse started, yet it has a tunnel", func() bool { return tunnel(b) })

	// a deleted: its open tunnel ends, and it does not get a new one.
	if err := nodes.Delete("a"); err != nil {
		t.Fatal(err)
	}
	nm.RefreshBlocklist(netID)
	gone := within(40*time.Second, "a's open tunnel ends once a is deleted", func() bool { return !tunnel(a) })
	t.Logf("a's tunnel ended %v after the refresh", gone.Round(100*time.Millisecond))
	never(5*time.Second, "a, deleted, got a new tunnel", func() bool { return tunnel(a) })

	// Expired entries go: Nebula refuses such certs anyway, so the list
	// stays small. Faked here by moving b's NotAfter back.
	exec(`UPDATE node_certs SET not_after = ? WHERE node_id = 'b'`,
		time.Now().Add(-nebulacfg.ClockSkewTolerance-time.Minute).Unix())
	nm.RefreshBlocklist(netID)
	inst.blockMu.Lock()
	left := len(inst.blocked)
	inst.blockMu.Unlock()
	if left != 1 {
		t.Errorf("%d entries after b's expired; want 1, a's", left)
	}
	back := within(20*time.Second, "b, no longer listed, gets a tunnel", func() bool { return tunnel(b) })
	t.Logf("b back %v after the refresh", back.Round(100*time.Millisecond))

	// A refresh that found the instance before the network was stopped
	// (the idle reaper, a deleted network) must not reload a closed Nebula.
	nm.StopNetwork(netID)
	nm.mu.Lock()
	nm.instances[netID] = inst
	nm.mu.Unlock()
	exec(`UPDATE node_certs SET not_after = ? WHERE node_id = 'a'`,
		time.Now().Add(-nebulacfg.ClockSkewTolerance-time.Minute).Unix())
	nm.RefreshBlocklist(netID)
	nm.mu.Lock()
	delete(nm.instances, netID)
	nm.mu.Unlock()
	inst.blockMu.Lock()
	left = len(inst.blocked)
	inst.blockMu.Unlock()
	if left != 1 {
		t.Errorf("a stopped lighthouse was reloaded: %d entries", left)
	}
}
