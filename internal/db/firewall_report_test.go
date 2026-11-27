package db

import (
	"testing"

	"github.com/trustos/hopssh/internal/fwpolicy"
)

// A device repeats its report in every heartbeat. The time stored is when it
// first made it, also after a restart of the server, which empties the
// store's cache: the statement itself must not overwrite an equal report.
func TestRecordReport_KeepsTheFirstTime(t *testing.T) {
	nodes, p := newIPTestStore(t)
	id := addIPTestNode(t, nodes, 2)
	reportedAt := func() int64 {
		t.Helper()
		var at int64
		if err := p.ReadDB.QueryRow(`SELECT reported_at FROM node_firewall WHERE node_id = ?`, id).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	set := func(at int64) {
		t.Helper()
		if _, err := p.WriteDB.Exec(`UPDATE node_firewall SET reported_at = ? WHERE node_id = ?`, at, id); err != nil {
			t.Fatal(err)
		}
	}
	ack := fwpolicy.Ack{Fingerprint: "ab"}

	if err := NewFirewallStore(p).RecordReport(id, ack); err != nil {
		t.Fatal(err)
	}
	set(1000)
	restarted := NewFirewallStore(p)
	if err := restarted.RecordReport(id, ack); err != nil {
		t.Fatal(err)
	}
	if at := reportedAt(); at != 1000 {
		t.Errorf("the same report after a restart moved its time to %d", at)
	}
	// Another report is another time; so is a refusal of the same policy.
	for _, next := range []fwpolicy.Ack{{Fingerprint: "cd"}, {Fingerprint: "cd", Error: "refused"}} {
		set(1000)
		if err := restarted.RecordReport(id, next); err != nil {
			t.Fatal(err)
		}
		if at := reportedAt(); at == 1000 {
			t.Errorf("report %+v kept the time of the one before", next)
		}
	}
	reports, err := restarted.Reports("net")
	if err != nil || reports[id] != (NodeReport{Fingerprint: "cd", Error: "refused", ReportedAt: reportedAt()}) {
		t.Errorf("Reports: %+v, %v", reports, err)
	}
}
