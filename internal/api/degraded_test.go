package api

import (
	"testing"
	"time"

	"github.com/trustos/hopssh/internal/db"
)

// Pointers to literals inside table-driven tests.
func ptrI64(v int64) *int64 { return &v }

// TestIsDegraded_TruthTable captures the full decision matrix for the
// degraded signal. Each case states the business-level scenario in the
// name — read the name first, then the field values.
func TestIsDegraded_TruthTable(t *testing.T) {
	now := time.Now().Unix()

	cases := []struct {
		name           string
		status         string
		nodeType       string
		createdAt      int64
		peersReportAt  *int64
		peersDirect    *int64
		peersRelayed   *int64
		peersInNetwork int
		want           bool
	}{
		{
			name: "not online — never degraded",
			status: "offline", nodeType: "node",
			createdAt: now - 3600, peersReportAt: ptrI64(now - 10),
			peersDirect: ptrI64(0), peersRelayed: ptrI64(0), peersInNetwork: 5,
			want: false,
		},
		{
			name: "lighthouse — never degraded (doesn't report peers about self)",
			status: "online", nodeType: "lighthouse",
			createdAt: now - 3600, peersReportAt: ptrI64(now - 10),
			peersDirect: ptrI64(0), peersRelayed: ptrI64(0), peersInNetwork: 5,
			want: false,
		},
		{
			name: "alone on network — no one to connect to, not degraded",
			status: "online", nodeType: "node",
			createdAt: now - 3600, peersReportAt: ptrI64(now - 10),
			peersDirect: ptrI64(0), peersRelayed: ptrI64(0), peersInNetwork: 0,
			want: false,
		},
		{
			name: "fresh agent within grace — not yet degraded",
			status: "online", nodeType: "node",
			createdAt: now - 60, peersReportAt: ptrI64(now - 10),
			peersDirect: ptrI64(0), peersRelayed: ptrI64(0), peersInNetwork: 2,
			want: false,
		},
		{
			name: "never reported peer state — insufficient signal",
			status: "online", nodeType: "node",
			createdAt: now - 3600, peersReportAt: nil,
			peersDirect: nil, peersRelayed: nil, peersInNetwork: 2,
			want: false,
		},
		{
			name: "stale peer report — insufficient signal",
			status: "online", nodeType: "node",
			createdAt: now - 3600, peersReportAt: ptrI64(now - 999),
			peersDirect: ptrI64(0), peersRelayed: ptrI64(0), peersInNetwork: 2,
			want: false,
		},
		{
			name: "has direct peer — healthy",
			status: "online", nodeType: "node",
			createdAt: now - 3600, peersReportAt: ptrI64(now - 10),
			peersDirect: ptrI64(1), peersRelayed: ptrI64(0), peersInNetwork: 2,
			want: false,
		},
		{
			name: "has relayed peer — healthy (suboptimal but reachable)",
			status: "online", nodeType: "node",
			createdAt: now - 3600, peersReportAt: ptrI64(now - 10),
			peersDirect: ptrI64(0), peersRelayed: ptrI64(1), peersInNetwork: 2,
			want: false,
		},
		{
			name: "degraded — all conditions met",
			status: "online", nodeType: "node",
			createdAt: now - 3600, peersReportAt: ptrI64(now - 10),
			peersDirect: ptrI64(0), peersRelayed: ptrI64(0), peersInNetwork: 2,
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isDegraded(tc.status, tc.nodeType, tc.createdAt, tc.peersReportAt, tc.peersDirect, tc.peersRelayed, tc.peersInNetwork)
			if got != tc.want {
				t.Fatalf("isDegraded = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCountPotentialPeers_ExcludesSelfAndLighthouseAndOffline captures
// the intended contract of the helper that feeds peersInNetwork. Wrong
// counts here would produce either false-positive degraded (counting a
// lighthouse as a potential peer) or false-negative (counting self).
func TestCountPotentialPeers_ExcludesSelfAndLighthouseAndOffline(t *testing.T) {
	now := time.Now().Unix()
	fresh := ptrI64(now - 10)
	stale := ptrI64(now - 9999)

	nodes := []*db.Node{
		{ID: "self", NodeType: "node", Status: "online", LastSeenAt: fresh},
		{ID: "peer-online-direct", NodeType: "node", Status: "online", LastSeenAt: fresh},
		{ID: "peer-online-relayed", NodeType: "node", Status: "online", LastSeenAt: fresh},
		{ID: "peer-offline", NodeType: "node", Status: "online", LastSeenAt: stale}, // effectiveStatus => offline
		{ID: "peer-pending", NodeType: "node", Status: "pending", LastSeenAt: nil},
		{ID: "lighthouse-1", NodeType: "lighthouse", Status: "online", LastSeenAt: fresh},
	}

	got := countPotentialPeers(nodes, "self", func(o *db.Node) (string, string, string, *int64) {
		return o.ID, o.NodeType, o.Status, o.LastSeenAt
	})
	// Expect 2: the two online non-lighthouse nodes that aren't self.
	if got != 2 {
		t.Fatalf("countPotentialPeers = %d, want 2 (self/lighthouse/stale/pending all excluded)", got)
	}
}
