package api_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/trustos/hopssh/internal/blocklist"
	"github.com/trustos/hopssh/internal/db"
	"github.com/trustos/hopssh/internal/e2etest"
	"github.com/trustos/hopssh/internal/nebulacfg"
)

// TestE2E_Blocklist: the heartbeat answer carries the certificates of the
// network's deleted nodes, which the devices give Nebula's pki.blocklist
// (docs/wiki/concepts/deleted-devices.md): every certificate the node got,
// at enrollment and at each renewal, until Nebula refuses it as expired.
// A node that renews too often is refused, so the list stays complete.
func TestE2E_Blocklist(t *testing.T) {
	h := newHousehold(t)
	box, nas, gone := h.owner.Enroll(h.net.ID, "box"), h.owner.Enroll(h.net.ID, "nas"), h.owner.Enroll(h.net.ID, "gone")
	far := h.outsider.CreateNetwork("far")
	farBox := h.outsider.Enroll(far.ID, "farbox")
	as := func(e e2etest.Enrollment) *e2etest.Client { c := h.s.Anonymous(); c.Token = e.AgentToken; return c }
	beat := func(e e2etest.Enrollment) []blocklist.Entry {
		t.Helper()
		var out struct{ Blocklist *[]blocklist.Entry }
		as(e).Want(200, "POST", "/api/heartbeat", M{"nodeId": e.NodeID}, &out)
		if out.Blocklist == nil {
			t.Fatal("the heartbeat answer has no blocklist; an empty one must be sent as [] (it replaces what the device holds)")
		}
		if err := blocklist.Check(*out.Blocklist); err != nil {
			t.Errorf("the server's list: %v", err)
		}
		return *out.Blocklist
	}
	fingerprint := func(pem string) (string, int64) {
		t.Helper()
		fp, notAfter, err := blocklist.Fingerprint([]byte(pem))
		if err != nil {
			t.Fatal(err)
		}
		return fp, notAfter.Add(nebulacfg.ClockSkewTolerance).Unix()
	}

	if l := beat(box); len(l) != 0 {
		t.Errorf("no node is deleted, yet the blocklist is %+v", l)
	}

	// gone renews once, so it holds two certificates when it is deleted.
	var renewed struct{ NodeCert string }
	as(gone).Want(200, "POST", "/api/renew", M{"nodeId": gone.NodeID}, &renewed)
	h.owner.Want(204, "DELETE", h.url("/nodes/%s", gone.NodeID), nil, nil)
	var want []blocklist.Entry
	for _, pem := range []string{gone.NodeCert, renewed.NodeCert} {
		fp, until := fingerprint(pem)
		want = append(want, blocklist.Entry{Fingerprint: fp, Until: until})
	}
	sortEntries := func(l []blocklist.Entry) {
		slices.SortFunc(l, func(a, b blocklist.Entry) int { return strings.Compare(a.Fingerprint, b.Fingerprint) })
	}
	sortEntries(want)
	for who, e := range map[string]e2etest.Enrollment{"box": box, "nas": nas} {
		got := beat(e)
		sortEntries(got)
		if !slices.Equal(got, want) {
			t.Errorf("gone deleted: %s's blocklist %+v; want gone's certificates %+v", who, got, want)
		}
	}
	if l := beat(farBox); len(l) != 0 {
		t.Errorf("the other network's blocklist has %+v", l)
	}
	for _, path := range []string{"/api/renew", "/api/heartbeat"} {
		if s, body := as(gone).Do("POST", path, M{"nodeId": gone.NodeID}); s != 401 {
			t.Errorf("%s of the deleted node: HTTP %d %s, want 401", path, s, body)
		}
	}

	// A flood of renewals is refused before its certificates could push one
	// of them off the list; nas holds its enrollment's certificate already.
	for i := 1; i < db.MaxHeldCerts; i++ {
		as(nas).Want(200, "POST", "/api/renew", M{"nodeId": nas.NodeID}, nil)
	}
	if s, body := as(nas).Do("POST", "/api/renew", M{"nodeId": nas.NodeID}); s != 429 {
		t.Errorf("renewal past %d certificates: HTTP %d %s, want 429", db.MaxHeldCerts, s, body)
	}
	h.owner.Want(204, "DELETE", h.url("/nodes/%s", nas.NodeID), nil, nil)
	if got := len(beat(box)); got != 2+db.MaxHeldCerts {
		t.Errorf("nas deleted: %d listed; want gone's 2 and nas's %d", got, db.MaxHeldCerts)
	}
}
