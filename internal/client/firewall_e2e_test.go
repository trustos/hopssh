package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Two real Nebula peers, each with the firewall block an enrollment writes,
// test what one lets through from the other.

// TestE2E_FirewallUDPBetweenAgents: in a network created with the "all"
// preset UDP passes between agents; in one with today's "tcp" block it stays
// dropped, as it always was.
func TestE2E_FirewallUDPBetweenAgents(t *testing.T) {
	for preset, want := range map[string]bool{firewallPresetAll: true, firewallPresetTCP: false} {
		t.Run(preset, func(t *testing.T) {
			a, ipB := firewallPair(t, agentFirewallFile(t, preset), agentFirewallFile(t, preset))
			if got := udpReaches(t, a, ipB+":9"); got != want {
				t.Errorf("UDP from a to b got through: %v, want %v", got, want)
			}
		})
	}
}

// TestE2E_KeepaliveReachesClient: an agent whose only peer is a `hop client
// join` device must not take the network for stuck. The keepalive dials TCP
// 41820 and counts only an answer, "refused" included, as alive.
func TestE2E_KeepaliveReachesClient(t *testing.T) {
	a, _ := firewallPair(t, agentFirewallFile(t, firewallPresetTCP), clientFirewallFile(t))
	probed, succeeded, _, peers := keepaliveOneCycle(a)
	if peers != 1 || probed != 1 || succeeded != 1 {
		t.Errorf("keepalive of an agent whose peer is a client: peers %d, probed %d, succeeded %d; want 1, 1, 1", peers, probed, succeeded)
	}
}

// firewallPair starts agent "a" and peer "b" with the given firewall blocks
// and waits until a has a tunnel to b. It returns a's instance and b's mesh
// address.
func firewallPair(t *testing.T, blockA, blockB string) (*meshInstance, string) {
	t.Helper()
	m := newE2EMesh(t)
	portA, portB := freeUDPPort(t), freeUDPPort(t)
	ipA, ipB := e2eSubnet+"6", e2eSubnet+"7"
	m.enroll("a", ipA, portA, time.Hour, map[string][]string{ipB: {localUDP(portB)}})
	m.enroll("b", ipB, portB, time.Hour, map[string][]string{ipA: {localUDP(portA)}})
	replaceFirewallSection(t, filepath.Join(m.dir, "a", "nebula.yaml"), blockA)
	replaceFirewallSection(t, filepath.Join(m.dir, "b", "nebula.yaml"), blockB)
	if err := m.c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := m.c.instances.get("a")
	// Any packet opens the tunnel, whether b's firewall lets it in or not.
	within(t, 20*time.Second, "a's tunnel to b", func() bool {
		_ = udpReaches(t, a, ipB+":9")
		ctrl := a.control()
		if ctrl == nil {
			return false
		}
		for _, h := range ctrl.ListHostmapHosts(false) {
			if len(h.VpnAddrs) > 0 && h.VpnAddrs[0].String() == ipB {
				return true
			}
		}
		return false
	})
	return a, ipB
}

// udpReaches sends one datagram over the mesh to a port where nothing
// listens. The far netstack answers with ICMP port unreachable, which every
// block lets back in and which reads here as "connection refused". A firewall
// that drops the datagram leaves the read to time out.
func udpReaches(t *testing.T, inst *meshInstance, target string) bool {
	t.Helper()
	d, ok := inst.currentSvc().(meshDialer)
	if !ok {
		t.Fatalf("%s has no userspace mesh", inst.name())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := d.DialContext(ctx, "udp", target)
	if err != nil {
		t.Fatalf("dial %s: %v", target, err)
	}
	defer conn.Close()
	// A read does not wake for the ICMP error; the next write returns it. So
	// each try has its own deadline: with one for both, an answer that came
	// after the first read began (half a second under -race) left the second
	// write to fail on the deadline instead.
	for i := 0; i < 2; i++ {
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		if _, err := conn.Write([]byte("x")); err != nil {
			return strings.Contains(err.Error(), "refused")
		}
		if _, err := conn.Read(make([]byte, 16)); err != nil && strings.Contains(err.Error(), "refused") {
			return true
		}
	}
	return false
}

// agentFirewallFile writes the nebula.yaml an agent enrollment renders for the
// preset and returns its path.
func agentFirewallFile(t *testing.T, preset string) string {
	t.Helper()
	dir := t.TempDir()
	writeNebulaConfig(dir, "10.42.1.1", "127.0.0.1", 42001, "userspace", 4242, preset)
	return filepath.Join(dir, "nebula.yaml")
}

// clientFirewallFile writes the nebula.yaml `hop client join` renders and
// returns its path.
func clientFirewallFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nebula.yaml")
	if err := os.WriteFile(path, []byte(clientNebulaConfig("10.42.1.1", "127.0.0.1", 42001, "")), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
