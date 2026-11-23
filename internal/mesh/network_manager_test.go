package mesh

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trustos/hopssh/internal/crypto"
	"github.com/trustos/hopssh/internal/db"
	"github.com/trustos/hopssh/internal/pki"
)

// A network that is still starting has no instance yet. Adding or deleting a
// node or DNS record in that moment must not crash the server.
func TestRefreshDNS_WhileStarting(t *testing.T) {
	nm := &NetworkManager{instances: map[string]*NetworkInstance{"starting": nil, "no-dns": {}}}
	for _, id := range []string{"starting", "no-dns", "unknown"} {
		nm.RefreshDNS(id)
	}
}

// Networks nobody uses are stopped to save memory, and come back for the first
// caller. v0.11.26 ended the whole server here ("exit status 2", eight times in
// production in June 2026), because closing a lighthouse could make Nebula call
// os.Exit; vendor patch 27. On Linux it also kept the port; vendor patch 31.
func TestReapIdleInstances(t *testing.T) {
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

	const never = -1
	now := time.Now().Unix()
	cases := []struct {
		id, node string // node's status, "" for none
		lastSeen int64
		reaped   bool
	}{
		{"network-empty", "", never, true},
		{"network-pending", "pending", never, false}, // about to enroll
		{"network-quiet", "offline", now - 3*3600, true},
		{"network-busy", "online", now, false},
	}
	ports := map[string]int{}
	for _, c := range cases {
		port := freePort(t)
		lhPort := int64(port)
		ports[c.id] = port
		subnet, err := networks.AllocateSubnet()
		if err != nil {
			t.Fatal(err)
		}
		ca, err := pki.GenerateCA(c.id, 2*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		ip, err := pki.ServerAddress(subnet)
		if err != nil {
			t.Fatal(err)
		}
		crt, err := pki.IssueCert(ca.CertPEM, ca.KeyPEM, "hopssh-server", ip, []string{"admin", "node"}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if err := networks.Create(&db.Network{ID: c.id, UserID: "owner", Name: c.id, Slug: c.id, NebulaSubnet: subnet,
			NebulaCACert: ca.CertPEM, NebulaCAKey: ca.KeyPEM, ServerCert: crt.CertPEM, ServerKey: crt.KeyPEM, LighthousePort: &lhPort}); err != nil {
			t.Fatal(err)
		}
		if c.node == "" {
			continue
		}
		if err := nodes.Create(&db.Node{ID: "node-" + c.id, NetworkID: c.id, Hostname: "box", AgentToken: "x", Status: c.node}); err != nil {
			t.Fatal(err)
		}
		exec(`UPDATE nodes SET status = ? WHERE network_id = ?`, c.node, c.id)
		if c.lastSeen != never {
			exec(`UPDATE nodes SET last_seen_at = ? WHERE network_id = ?`, c.lastSeen, c.id)
		}
	}

	nm, err := NewNetworkManager(networks, nodes, db.NewDNSRecordStore(pair))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nm.Stop)
	for _, c := range cases {
		if portFree(ports[c.id]) {
			t.Fatalf("%s did not start", c.id)
		}
	}
	for round := range 10 {
		// Keep the hostmap lock busy, as peers do: it widens the window in
		// which the crash happened.
		stop := make(chan struct{})
		for _, c := range cases {
			inst, err := nm.GetInstance(c.id)
			if err != nil {
				t.Fatalf("round %d: %s: %v", round, c.id, err)
			}
			if portFree(ports[c.id]) {
				t.Fatalf("round %d: %s is not running", round, c.id)
			}
			for range 4 {
				go func() {
					for {
						select {
						case <-stop:
							return
						default:
							inst.ctrl.ListHostmapIndexes(false)
						}
					}
				}()
			}
		}
		time.Sleep(2 * time.Millisecond)
		nm.reapIdleInstances(2 * time.Hour)
		close(stop)
		for _, c := range cases {
			if got := portFree(ports[c.id]); got != c.reaped {
				t.Fatalf("round %d: %s: stopped = %v, want %v", round, c.id, got, c.reaped)
			}
		}
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	p, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	return p.LocalAddr().(*net.UDPAddr).Port
}

// portFree reports whether nothing listens on a UDP port. Nebula's socket is
// IPv4, so an IPv4 one is needed to see it (a dual-stack one isn't refused).
func portFree(port int) bool {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
	if err != nil {
		return false
	}
	c.Close()
	return true
}
