package api_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trustos/hopssh/internal/e2etest"
)

// TestE2E_SelfEndpoints: the addresses a device reports in its heartbeat
// (selfEndpoints) are sent to every other device of its network, which
// gives them to Nebula and writes them to its peer cache. The server keeps
// at most the 10 per address family that Nebula would use
// (nebula.MaxRemotes), in the order sent, and only addresses a peer can
// send to: not port 0, with a zone, loopback, unspecified, multicast,
// link-local, 0.0.0.0/8, 240.0.0.0/4 or a mesh address. Until
// 2026-11-28 it kept them as sent: one device could put a megabyte in every
// answer to its peers, and junk in front pushed the real addresses out of
// the 10 that Nebula takes.
func TestE2E_SelfEndpoints(t *testing.T) {
	h := newHousehold(t)
	box, nas := h.owner.Enroll(h.net.ID, "box"), h.owner.Enroll(h.net.ID, "nas")
	boxIP := strings.TrimSuffix(box.NebulaIP, "/24")
	mesh9 := box.NebulaIP[:strings.LastIndex(box.NebulaIP, ".")] + ".9" // in this network's mesh
	beat := func(token, nodeID string, endpoints []string, lifetimes []int) map[string][]string {
		t.Helper()
		c := h.s.Anonymous()
		c.Token = token
		var out struct{ PeerEndpoints map[string][]string }
		c.Want(200, "POST", "/api/heartbeat", M{"nodeId": nodeID, "selfEndpoints": endpoints, "selfEndpointLifetimesSec": lifetimes}, &out)
		return out.PeerEndpoints
	}

	sent := []string{
		"203.0.113.7:4242", // the NAT-PMP mapping comes first
		"nope",
		"203.0.113.9:0",
		"0.0.0.0:4242",
		"127.0.0.1:4242",
		"224.0.0.1:4242",
		"239.1.2.3:4242", // multicast, not link-local
		"169.254.1.1:4242",
		"255.255.255.255:4242",
		"0.1.2.3:4242",
		"240.0.0.1:4242",
		mesh9 + ":4242",
		"[::ffff:" + mesh9 + "]:4242",
		"10.42.200.1:4242", // another network's mesh
		"[::1]:4242",
		"[::]:4242",
		"[::%eth0]:4242",
		"[fe80::1]:4242",
		"[ff02::1]:4242",
		"[ff0e::1]:4242",
		"203.0.113.7:4242",          // again
		"[::ffff:203.0.113.7]:4242", // again, as IPv6
		"[2001:db8::7%eth0]:4242",   // a zone: the same address, or 1 KB
		"[2001:db8::8%" + strings.Repeat("x", 1000) + "]:4242",
		"[2001:db8::7]:4242",
	}
	for i := 1; i <= 12; i++ {
		sent = append(sent, fmt.Sprintf("192.168.1.%d:4242", i))
	}
	for i := 1; i <= 11; i++ { // [2001:db8::7] again among them
		sent = append(sent, fmt.Sprintf("[2001:db8::%x]:4242", i))
	}
	lifetimes := make([]int, len(sent))
	lifetimes[0] = 3600
	beat(box.AgentToken, box.NodeID, sent, lifetimes)

	want := []string{"203.0.113.7:4242", "[2001:db8::7]:4242"}
	for i := 1; i <= 9; i++ {
		want = append(want, fmt.Sprintf("192.168.1.%d:4242", i))
	}
	for _, i := range []int{1, 2, 3, 4, 5, 6, 8, 9, 10} {
		want = append(want, fmt.Sprintf("[2001:db8::%x]:4242", i))
	}
	// box is a peer once its heartbeat reached the database (within 5 s).
	var got []string
	e2etest.Within(t, 20*time.Second, "box to be a peer of nas", func() bool {
		got = beat(nas.AgentToken, nas.NodeID, nil, nil)[boxIP]
		return len(got) > 0
	})
	if !slices.Equal(got, want) {
		t.Errorf("nas is told box is at\n %q\nwant\n %q", got, want)
	}

	// Nothing usable replaces what box said before.
	beat(box.AgentToken, box.NodeID, []string{"127.0.0.1:4242", "nope"}, nil)
	if got := beat(nas.AgentToken, nas.NodeID, nil, nil)[boxIP]; len(got) != 0 {
		t.Errorf("after a heartbeat with nothing usable, nas is told box is at %q", got)
	}
}
