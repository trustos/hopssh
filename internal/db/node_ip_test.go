package db

import (
	"fmt"
	"testing"
	"time"
)

// A deleted node's certificate stays valid until it expires, up to 24 h
// later, and Nebula asks no database. NextNodeIndex took the highest address
// among the nodes still in the table, so deleting the newest node gave its
// address to the next one: two valid certificates on one address, which
// peers cannot tell apart and a rule for one matches both.

func newIPTestStore(t *testing.T) (*NodeStore, *DBPair) {
	t.Helper()
	p, enc := openTestDB(t)
	addTestNetwork(t, p, enc, "net")
	return NewNodeStore(p, enc), p
}

func addIPTestNode(t *testing.T, s *NodeStore, octet int) string {
	t.Helper()
	id := fmt.Sprintf("node-%d", octet)
	if err := s.Create(&Node{ID: id, NetworkID: "net", NebulaIP: fmt.Sprintf("10.42.1.%d/24", octet),
		AgentToken: "tok", NodeType: "node", Status: "enrolled"}); err != nil {
		t.Fatal(err)
	}
	return id
}

// nextOctet is the last octet of the address the next node gets.
func nextOctet(t *testing.T, s *NodeStore) int {
	t.Helper()
	idx, err := s.NextNodeIndex("net")
	if err != nil {
		t.Fatalf("NextNodeIndex: %v", err)
	}
	return idx + 2 // pki.NodeAddress
}

func TestNextNodeIndex_DeletedNewestIsNotReused(t *testing.T) {
	s, _ := newIPTestStore(t)
	if got := nextOctet(t, s); got != 2 {
		t.Fatalf("first node gets .%d, want .2", got)
	}
	addIPTestNode(t, s, 2)
	newest := addIPTestNode(t, s, 3)
	if err := s.Delete(newest); err != nil {
		t.Fatal(err)
	}
	if got := nextOctet(t, s); got != 4 {
		t.Errorf("after deleting .3 the next node gets .%d, want .4: .3's certificate is still valid", got)
	}
}

// TestNextNodeIndex_AnyDeletionCounts: the address is recorded by the
// database, not by the code path, so pruning, a raw DELETE and the cascade of
// a deleted network all count.
func TestNextNodeIndex_AnyDeletionCounts(t *testing.T) {
	s, p := newIPTestStore(t)
	addIPTestNode(t, s, 2)
	addIPTestNode(t, s, 3)
	if _, err := p.WriteDB.Exec(`DELETE FROM nodes WHERE id = 'node-3'`); err != nil {
		t.Fatal(err)
	}
	if got := nextOctet(t, s); got != 4 {
		t.Errorf("after a raw DELETE of .3 the next node gets .%d, want .4", got)
	}
}

// TestNextNodeIndex_FullNetwork: past the end of the /24 an address comes
// back, but only one released longer ago than a certificate lives.
func TestNextNodeIndex_FullNetwork(t *testing.T) {
	s, p := newIPTestStore(t)
	for o := 2; o <= 254; o++ {
		addIPTestNode(t, s, o)
	}
	for _, o := range []int{100, 200} {
		if err := s.Delete(fmt.Sprintf("node-%d", o)); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-releasedIPQuarantine - time.Hour).Unix()
	if _, err := p.WriteDB.Exec(`UPDATE released_node_ips SET released_at = ? WHERE nebula_ip = '10.42.1.200/24'`, old); err != nil {
		t.Fatal(err)
	}
	if got := nextOctet(t, s); got != 200 {
		t.Fatalf("full network: next node gets .%d, want .200 (released long ago; .100 is recent)", got)
	}
	addIPTestNode(t, s, 200)
	if idx, err := s.NextNodeIndex("net"); err == nil {
		t.Errorf("only .100 is free, released just now: got .%d, want an error", idx+2)
	}
}

func TestDeleteNetwork_LeavesNoReleasedAddresses(t *testing.T) {
	s, p := newIPTestStore(t)
	addIPTestNode(t, s, 2)
	gone := addIPTestNode(t, s, 3)
	if err := s.Delete(gone); err != nil {
		t.Fatal(err)
	}
	if err := NewNetworkStore(p, nil).Delete("net"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := p.ReadDB.QueryRow(`SELECT COUNT(*) FROM released_node_ips WHERE network_id = 'net'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d released addresses of a deleted network left", n)
	}
}
