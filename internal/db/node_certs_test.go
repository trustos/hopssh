package db

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/trustos/hopssh/internal/blocklist"
	"github.com/trustos/hopssh/internal/nebulacfg"
	"github.com/trustos/hopssh/internal/pki"
)

// certsTest is a network with a CA, and a node store.
type certsTest struct {
	t     *testing.T
	p     *DBPair
	nodes *NodeStore
	ca    *pki.CertBundle
}

func newCertsTest(t *testing.T, networks ...string) *certsTest {
	t.Helper()
	p, enc := openTestDB(t)
	for _, n := range networks {
		addTestNetwork(t, p, enc, n)
	}
	ca, err := pki.GenerateCA("test", 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return &certsTest{t: t, p: p, nodes: NewNodeStore(p, enc), ca: ca}
}

func (c *certsTest) node(network, id string) {
	c.t.Helper()
	if err := c.nodes.Create(&Node{ID: id, NetworkID: network, Hostname: id, AgentToken: "t", Status: "pending"}); err != nil {
		c.t.Fatal(err)
	}
}

// cert issues a cert and gives its PEM and fingerprint.
func (c *certsTest) cert(d time.Duration) ([]byte, string) {
	c.t.Helper()
	nc, err := pki.IssueCert(c.ca.CertPEM, c.ca.KeyPEM, "n", netip.MustParsePrefix("10.42.9.7/24"), []string{"node"}, d)
	if err != nil {
		c.t.Fatal(err)
	}
	fp, _, err := blocklist.Fingerprint(nc.CertPEM)
	if err != nil {
		c.t.Fatal(err)
	}
	return nc.CertPEM, fp
}

func (c *certsTest) list(network string, now time.Time) []string {
	c.t.Helper()
	l, err := c.nodes.Blocklist(network, now)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := blocklist.Check(l); err != nil {
		c.t.Errorf("the server's own list: %v", err)
	}
	var out []string
	for _, e := range l {
		out = append(out, e.Fingerprint)
	}
	slices.Sort(out) // the order is the server's, newest first
	return out
}

func (c *certsTest) rows() int {
	c.t.Helper()
	var n int
	if err := c.p.ReadDB.QueryRow(`SELECT count(*) FROM node_certs`).Scan(&n); err != nil {
		c.t.Fatal(err)
	}
	return n
}

// TestBlocklist_DeletedNodesCerts: every cert given a node is recorded; once
// the node is gone, all of them are the network's blocklist, until Nebula
// refuses them as expired; a node that exists is never on it.
func TestBlocklist_DeletedNodesCerts(t *testing.T) {
	c := newCertsTest(t, "net", "other")
	c.node("net", "a")
	c.node("net", "b")
	c.node("other", "x")
	a1, fpA1 := c.cert(24 * time.Hour)
	a2, fpA2 := c.cert(24 * time.Hour)
	b1, fpB1 := c.cert(24 * time.Hour)
	x1, _ := c.cert(24 * time.Hour)
	for _, step := range []struct {
		name string
		err  error
	}{
		{"a enrolls", c.nodes.CompleteEnrollment("a", a1, []byte("key"), "a", "linux", "arm64")},
		{"a renews", c.nodes.UpdateCert("a", a2, []byte("key"))},
		{"b enrolls", c.nodes.CompleteEnrollment("b", b1, []byte("key"), "b", "linux", "arm64")},
		{"x enrolls", c.nodes.CompleteEnrollment("x", x1, []byte("key"), "x", "linux", "arm64")},
	} {
		if step.err != nil {
			t.Fatalf("%s: %v", step.name, step.err)
		}
	}
	if got := c.rows(); got != 4 {
		t.Errorf("%d certs recorded; want 4", got)
	}
	now := time.Now()
	if got := c.list("net", now); len(got) != 0 {
		t.Errorf("no node is deleted, yet the blocklist is %q", got)
	}

	if err := c.nodes.Delete("a"); err != nil {
		t.Fatal(err)
	}
	want := []string{fpA1, fpA2}
	slices.Sort(want)
	if got := c.list("net", now); !slices.Equal(got, want) {
		t.Errorf("a deleted: blocklist %q; want both of a's certs %q", got, want)
	}
	if got := c.list("other", now); len(got) != 0 {
		t.Errorf("the other network's blocklist has %q", got)
	}
	l, _ := c.nodes.Blocklist("net", now)
	_, notAfter, _ := blocklist.Fingerprint(a1)
	if want := notAfter.Add(nebulacfg.ClockSkewTolerance).Unix(); l[0].Until != want && l[1].Until != want {
		t.Errorf("until %d, %d; want NotAfter plus the skew tolerance, %d", l[0].Until, l[1].Until, want)
	}

	// Nebula refuses the certs after NotAfter plus the tolerance: then they
	// need no blocking.
	if got := c.list("net", notAfter.Add(nebulacfg.ClockSkewTolerance-time.Minute)); len(got) != 2 {
		t.Errorf("a minute before Nebula refuses them: %d listed; want 2", len(got))
	}
	if got := c.list("net", notAfter.Add(nebulacfg.ClockSkewTolerance+time.Minute)); len(got) != 0 {
		t.Errorf("after Nebula refuses them anyway: %q", got)
	}

	// Every way of deleting counts: a prune too.
	if _, err := c.p.WriteDB.Exec(`DELETE FROM nodes WHERE id = 'b'`); err != nil {
		t.Fatal(err)
	}
	if got := c.list("net", now); !slices.Contains(got, fpB1) || len(got) != 3 {
		t.Errorf("b deleted by hand: %q; want b's cert as well", got)
	}
}

// TestUpdateCert_NodeGone: a renewal that races a delete must not store a
// cert, as no blocklist would know it; the handler then hands out nothing.
func TestUpdateCert_NodeGone(t *testing.T) {
	c := newCertsTest(t, "net")
	pem, _ := c.cert(time.Hour)
	if err := c.nodes.UpdateCert("never", pem, []byte("key")); !errors.Is(err, ErrNodeGone) {
		t.Errorf("UpdateCert of no node: %v; want ErrNodeGone", err)
	}
	if err := c.nodes.CompleteEnrollment("never", pem, []byte("key"), "h", "linux", "arm64"); !errors.Is(err, ErrNodeGone) {
		t.Errorf("CompleteEnrollment of no node: %v; want ErrNodeGone", err)
	}
	if got := c.rows(); got != 0 {
		t.Errorf("%d certs recorded for no node", got)
	}
}

// TestUpdateCert_TooMany: a node holds at most MaxHeldCerts certs that
// Nebula accepts. A renewal past that is refused and not stored, so a flood
// of renewals with a stolen token cannot push one of its certs off the
// blocklist, which is cut at blocklist.MaxEntries.
func TestUpdateCert_TooMany(t *testing.T) {
	c := newCertsTest(t, "net")
	c.node("net", "a")
	var held []string
	for i := range MaxHeldCerts {
		pem, fp := c.cert(time.Hour)
		if err := c.nodes.UpdateCert("a", pem, []byte("key")); err != nil {
			t.Fatalf("renewal %d: %v", i+1, err)
		}
		held = append(held, fp)
	}
	extra, fpExtra := c.cert(time.Hour)
	if err := c.nodes.UpdateCert("a", extra, []byte("key")); !errors.Is(err, ErrTooManyCerts) {
		t.Fatalf("renewal %d: %v; want ErrTooManyCerts", MaxHeldCerts+1, err)
	}
	if n, err := c.nodes.Get("a"); err != nil || string(n.NebulaCert) == string(extra) {
		t.Errorf("the refused cert was stored (%v)", err)
	}
	if got := c.rows(); got != MaxHeldCerts {
		t.Errorf("%d certs recorded; want %d", got, MaxHeldCerts)
	}

	// A cert Nebula refuses anyway does not count.
	if _, err := c.p.WriteDB.Exec(`UPDATE node_certs SET not_after = ? WHERE fingerprint = ?`,
		time.Now().Add(-nebulacfg.ClockSkewTolerance-time.Minute).Unix(), held[0]); err != nil {
		t.Fatal(err)
	}
	if err := c.nodes.UpdateCert("a", extra, []byte("key")); err != nil {
		t.Fatalf("once one cert expired: %v", err)
	}
	c.nodes.Delete("a")
	want := append(held[1:], fpExtra)
	slices.Sort(want)
	if got := c.list("net", time.Now()); !slices.Equal(got, want) {
		t.Errorf("a deleted: %d listed; want the %d certs it holds", len(got), len(want))
	}
}

// TestBlocklist_Full: past blocklist.MaxEntries the list keeps the certs
// that expire last, those of the devices deleted last.
func TestBlocklist_Full(t *testing.T) {
	c := newCertsTest(t, "net")
	now := time.Now()
	tx, err := c.p.WriteDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	const over = 3
	for i := range blocklist.MaxEntries + over {
		if _, err := tx.Exec(`INSERT INTO node_certs (fingerprint, network_id, node_id, not_after) VALUES (?, 'net', 'gone', ?)`,
			fmt.Sprintf("%064x", i), now.Unix()+int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	l, err := c.nodes.Blocklist("net", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := blocklist.Check(l); err != nil {
		t.Fatalf("the server's own list: %v", err)
	}
	if len(l) != blocklist.MaxEntries {
		t.Fatalf("%d listed; want %d", len(l), blocklist.MaxEntries)
	}
	tol := int64(nebulacfg.ClockSkewTolerance / time.Second)
	for _, e := range l {
		if e.Until-tol < now.Unix()+over {
			t.Fatalf("%s is listed, one of the %d that expire first", e.Fingerprint, over)
		}
	}
}

// TestUpdateCert_UnreadableCert: a cert the blocklist could not name is not
// stored either; the node keeps the one it had.
func TestUpdateCert_UnreadableCert(t *testing.T) {
	c := newCertsTest(t, "net")
	c.node("net", "a")
	good, fp := c.cert(time.Hour)
	if err := c.nodes.UpdateCert("a", good, []byte("key")); err != nil {
		t.Fatal(err)
	}
	if err := c.nodes.UpdateCert("a", []byte("not a cert"), []byte("key")); err == nil {
		t.Error("an unreadable cert was stored")
	}
	n, err := c.nodes.Get("a")
	if err != nil {
		t.Fatal(err)
	}
	if string(n.NebulaCert) != string(good) {
		t.Error("the node lost the cert it had")
	}
	c.nodes.Delete("a")
	if got := c.list("net", time.Now()); !slices.Equal(got, []string{fp}) {
		t.Errorf("blocklist %q; want %q", got, fp)
	}
}

// TestRecordCert_PrunesExpired: rows Nebula refuses anyway go when the next
// cert is recorded, so the table stays small.
func TestRecordCert_PrunesExpired(t *testing.T) {
	c := newCertsTest(t, "net")
	c.node("net", "a")
	old := time.Now().Add(-nebulacfg.ClockSkewTolerance - time.Minute).Unix()
	if _, err := c.p.WriteDB.Exec(`INSERT INTO node_certs (fingerprint, network_id, node_id, not_after) VALUES ('old', 'net', 'gone', ?)`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := c.p.WriteDB.Exec(`INSERT INTO node_certs (fingerprint, network_id, node_id, not_after) VALUES ('recent', 'net', 'gone', ?)`, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	pem, _ := c.cert(time.Hour)
	if err := c.nodes.UpdateCert("a", pem, []byte("key")); err != nil {
		t.Fatal(err)
	}
	var fps []string
	rows, err := c.p.ReadDB.Query(`SELECT fingerprint FROM node_certs ORDER BY fingerprint`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		rows.Scan(&s)
		fps = append(fps, s)
	}
	if slices.Contains(fps, "old") || !slices.Contains(fps, "recent") || len(fps) != 2 {
		t.Errorf("rows %q; want the recent one and a's, not the old one", fps)
	}
}

// TestRecordHeldCerts: nodes that got their cert before migration 013 have
// it recorded at start, so deleting one blocks it.
func TestRecordHeldCerts(t *testing.T) {
	c := newCertsTest(t, "net")
	c.node("net", "a")
	c.node("net", "b")
	c.node("net", "pending")
	pem, fp := c.cert(time.Hour)
	if _, err := c.p.WriteDB.Exec(`UPDATE nodes SET nebula_cert = ? WHERE id = 'a'`, pem); err != nil {
		t.Fatal(err)
	}
	if _, err := c.p.WriteDB.Exec(`UPDATE nodes SET nebula_cert = ? WHERE id = 'b'`, []byte("not a cert")); err != nil {
		t.Fatal(err)
	}
	n, err := c.nodes.RecordHeldCerts()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("recorded %d; want 1 (a's; b's cannot be read, the pending node has none)", n)
	}
	if n, err := c.nodes.RecordHeldCerts(); err != nil || n != 0 {
		t.Errorf("again: %d, %v; want 0", n, err)
	}
	c.nodes.Delete("a")
	if got := c.list("net", time.Now()); !slices.Equal(got, []string{fp}) {
		t.Errorf("blocklist %q; want a's cert", got)
	}
}
