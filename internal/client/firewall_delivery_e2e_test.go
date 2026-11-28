package client

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trustos/hopssh/internal/fwpolicy"
)

// TestE2E_FirewallDelivery: phase 2 of docs/wiki/concepts/firewall-rules.md,
// with two real Nebula peers. Device b gets its network's policy in its
// heartbeat answer and applies it in place within that heartbeat: a rule
// that goes stops new and open flows, the tunnel stays (no new handshake),
// and a policy b refuses leaves nebula.yaml byte for byte and the rules it
// runs. Its next heartbeat says what it runs.
func TestE2E_FirewallDelivery(t *testing.T) {
	var policy atomic.Pointer[fwpolicy.Delivered]
	var report atomic.Pointer[fwpolicy.Ack] // b's last, nil when it sent none
	m, a, b, ipB := firewallMesh(t, agentFirewallFile(t, firewallPresetAll), agentFirewallFile(t, firewallPresetPing), func(m *e2eMesh) {
		fn := func(body map[string]any) any {
			if body["nodeId"] != "node-b" {
				return map[string]any{}
			}
			var r *fwpolicy.Ack
			if raw, ok := body["firewall"]; ok {
				b, _ := json.Marshal(raw)
				r = new(fwpolicy.Ack)
				json.Unmarshal(b, r)
			}
			report.Store(r)
			if p := policy.Load(); p != nil {
				return map[string]any{"firewall": p}
			}
			return map[string]any{}
		}
		m.onHeartbeat.Store(&fn)
	})
	ipA := e2eSubnet + "6"
	file := filepath.Join(m.dir, "b", "nebula.yaml")
	beat := func() {
		t.Helper()
		if err := sendHeartbeat(b); err != nil {
			t.Fatalf("b's heartbeat: %v", err)
		}
	}
	reported := func(want fwpolicy.Ack, errorHas string) {
		t.Helper()
		beat()
		r := report.Load()
		if r == nil || r.Fingerprint != want.Fingerprint || (errorHas == "") != (r.Error == "") || !strings.Contains(r.Error, errorHas) {
			t.Errorf("b reports %+v; want fingerprint %.12s and an error with %q", r, want.Fingerprint, errorHas)
		}
	}
	// a's tunnel to b, by its index: a new handshake makes a new one.
	tunnel := func() uint32 {
		t.Helper()
		for _, h := range a.control().ListHostmapHosts(false) {
			if len(h.VpnAddrs) > 0 && h.VpnAddrs[0].String() == ipB {
				return h.LocalIndex
			}
		}
		t.Fatal("a has no tunnel to b")
		return 0
	}
	dial := func(port string) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return a.currentSvc().(meshDialer).DialContext(ctx, "tcp", ipB+":"+port)
	}
	udp9 := fwpolicy.DeliveredRule{Proto: "udp", Port: "9", Source: ipA}
	tcp7000 := fwpolicy.DeliveredRule{Proto: "tcp", Port: "7000"}

	// The block b was enrolled with: ICMP and the agent API only.
	if udpReaches(t, a, ipB+":9") {
		t.Fatal("the ping block lets UDP from a in")
	}
	beat()
	if r := report.Load(); r != nil {
		t.Errorf("b reports %+v before any policy came", r)
	}
	start := tunnel()

	// A policy: UDP 9 from a, TCP 7000 from every node.
	p1 := delivered(fwpolicy.PresetPing, udp9, tcp7000)
	policy.Store(&p1)
	beat()
	if !udpReaches(t, a, ipB+":9") {
		t.Error("UDP 9 from a does not reach b after the policy that allows it")
	}
	rules := firewallRules(t, readFile(t, file))
	for _, want := range []string{
		"in udp 9 cidr=" + ipA + "/32", "in tcp 7000 groups=node", // the policy
		"in tcp 41820 groups=admin", "in tcp 41820 groups=node", "in icmp any host=any", "in default drop", // always
	} {
		if !slices.Contains(rules, want) {
			t.Errorf("nebula.yaml has no %q: %q", want, rules)
		}
	}
	reported(fwpolicy.Ack{Fingerprint: p1.Fingerprint}, "")
	if _, succeeded, _, _ := keepaliveOneCycle(a); succeeded != 1 {
		t.Errorf("a's keepalive does not reach b under the policy: succeeded %d", succeeded)
	}

	// An open flow, then a policy without its rule: it stops at its next
	// packet, and a new one does not start.
	ln, err := b.currentSvc().Listen("tcp", ":7000")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				s := bufio.NewScanner(c)
				for s.Scan() {
					c.Write([]byte(s.Text() + "\n"))
				}
			}()
		}
	}()
	conn, err := dial("7000")
	if err != nil {
		t.Fatalf("TCP 7000 under a policy that allows it: %v", err)
	}
	defer conn.Close()
	echo := func(s string) error {
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := conn.Write([]byte(s + "\n")); err != nil {
			return err
		}
		_, err := bufio.NewReader(conn).ReadString('\n')
		return err
	}
	if err := echo("one"); err != nil {
		t.Fatalf("the open flow before the change: %v", err)
	}
	p2 := delivered(fwpolicy.PresetPing, udp9)
	policy.Store(&p2)
	beat()
	if err := echo("two"); err == nil {
		t.Error("the open flow goes on after its rule went")
	}
	if c, err := dial("7000"); err == nil {
		c.Close()
		t.Error("a new flow starts after its rule went")
	}
	if !udpReaches(t, a, ipB+":9") {
		t.Error("UDP 9 from a stopped with a policy that keeps it")
	}
	reported(fwpolicy.Ack{Fingerprint: p2.Fingerprint}, "")

	// Policies b refuses: nothing changes, and the report says why.
	outside := delivered(fwpolicy.PresetPing, fwpolicy.DeliveredRule{Proto: "udp", Port: "9", Source: "10.99.0.5"})
	forged := delivered(fwpolicy.PresetAll)
	forged.Rules = []fwpolicy.DeliveredRule{tcp7000}
	unknown := delivered("everything")
	for name, c := range map[string]struct {
		p   fwpolicy.Delivered
		why string
	}{"a source outside the network": {outside, "is not an address of"}, "a fingerprint of another policy": {forged, "fingerprint"}, "an unknown preset": {unknown, "preset"}} {
		before := readFile(t, file)
		policy.Store(&c.p)
		beat()
		if readFile(t, file) != before {
			t.Errorf("%s: nebula.yaml changed", name)
		}
		if !udpReaches(t, a, ipB+":9") {
			t.Errorf("%s: the rules b ran before are gone", name)
		}
		reported(fwpolicy.Ack{Fingerprint: p2.Fingerprint}, c.why)
	}

	// An answer without a policy changes nothing.
	before := readFile(t, file)
	policy.Store(nil)
	beat()
	if readFile(t, file) != before || !udpReaches(t, a, ipB+":9") {
		t.Error("an answer without a policy changed the firewall")
	}

	// The policy b runs, again after a refusal: applied, no error.
	policy.Store(&p2)
	beat()
	reported(fwpolicy.Ack{Fingerprint: p2.Fingerprint}, "")

	// A start in progress: the beat leaves b alone, and its report stays.
	// The next beat after it applies the policy.
	before = readFile(t, file)
	b.startMu.Lock()
	policy.Store(&p1)
	beat()
	if readFile(t, file) != before {
		t.Error("a policy came during a start: nebula.yaml changed")
	}
	if c, err := dial("7000"); err == nil {
		c.Close()
		t.Error("a policy came during a start: it runs")
	}
	reported(fwpolicy.Ack{Fingerprint: p2.Fingerprint}, "")
	b.startMu.Unlock()
	beat()
	if c, err := dial("7000"); err != nil {
		t.Errorf("the policy that waited for a start: TCP 7000 does not reach b: %v", err)
	} else {
		c.Close()
	}
	reported(fwpolicy.Ack{Fingerprint: p1.Fingerprint}, "")

	// nebula.yaml changed since Nebula loaded it: a reload would bring the
	// rest of the change too, so the policy waits until the file is as
	// loaded again.
	orig := readFile(t, file)
	drifted := orig + "\nhopssh_test: 1\n"
	if err := os.WriteFile(file, []byte(drifted), 0644); err != nil {
		t.Fatal(err)
	}
	policy.Store(&p2)
	beat()
	if readFile(t, file) != drifted {
		t.Error("a policy came after nebula.yaml changed: the file was written")
	}
	if c, err := dial("7000"); err != nil {
		t.Errorf("a policy came after nebula.yaml changed: the rules b ran are gone: %v", err)
	} else {
		c.Close()
	}
	reported(fwpolicy.Ack{Fingerprint: p1.Fingerprint}, "")
	if err := os.WriteFile(file, []byte(orig), 0644); err != nil {
		t.Fatal(err)
	}
	beat()
	if c, err := dial("7000"); err == nil {
		c.Close()
		t.Error("the policy that waited for the file: TCP 7000 still reaches b")
	}
	reported(fwpolicy.Ack{Fingerprint: p2.Fingerprint}, "")

	// nebula.yaml can't be written: the rules b ran come back, the report
	// says why, and the policy is not tried again on this Nebula. Root
	// writes into a read-only directory, and Windows has no such mode.
	dir := filepath.Dir(file)
	if os.Getuid() > 0 {
		before = readFile(t, file)
		if err := os.Chmod(dir, 0500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(dir, 0700) })
		policy.Store(&p1)
		beat()
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if readFile(t, file) != before {
			t.Error("a policy that was not written: nebula.yaml changed")
		}
		if c, err := dial("7000"); err == nil {
			c.Close()
			t.Error("a policy that was not written: it runs")
		}
		if !udpReaches(t, a, ipB+":9") {
			t.Error("a policy that was not written: the rules b ran are gone")
		}
		reported(fwpolicy.Ack{Fingerprint: p2.Fingerprint}, "write nebula.yaml")
		// That beat could write the file again; a report is the state before
		// its beat, so what runs is what shows a second try.
		if c, err := dial("7000"); err == nil {
			c.Close()
			t.Error("a policy refused on this Nebula was tried again")
		}
	}

	// Only the backup can't be written: the file and Nebula have the
	// policy, so it is applied.
	p3 := delivered(fwpolicy.PresetPing, udp9, tcp7000, fwpolicy.DeliveredRule{Proto: "tcp", Port: "7001"})
	bak := file + ".bak"
	if err := os.Remove(bak); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(bak, 0700); err != nil {
		t.Fatal(err)
	}
	policy.Store(&p3)
	beat()
	if !slices.Contains(firewallRules(t, readFile(t, file)), "in tcp 7001 groups=node") {
		t.Error("a policy whose backup was not written: nebula.yaml does not have it")
	}
	if c, err := dial("7000"); err != nil {
		t.Errorf("a policy whose backup was not written: it does not run: %v", err)
	} else {
		c.Close()
	}
	reported(fwpolicy.Ack{Fingerprint: p3.Fingerprint}, "")
	if err := os.Remove(bak); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(file + "*.tmp-*"); len(left) > 0 {
		t.Errorf("temporary files left: %q", left)
	}

	if got := tunnel(); got != start {
		t.Errorf("a's tunnel to b is a new one (index %d, was %d): a reload made a handshake", got, start)
	}
}

// TestE2E_FirewallTCPNetworkToAll: a network from before migration 009 moved
// to "Everything" in its Firewall tab (item 4 of the plan), with two real
// Nebula peers. Both joined it with the TCP-only block. The first save makes
// the network managed, and b's next heartbeat answer carries the preset with
// no rules: from that beat on UDP from a reaches b. Back to "TCP only", it
// stops again while TCP still gets through. The tunnel stays throughout.
func TestE2E_FirewallTCPNetworkToAll(t *testing.T) {
	var policy atomic.Pointer[fwpolicy.Delivered]
	var report atomic.Pointer[fwpolicy.Ack]
	m, a, b, ipB := firewallMesh(t, agentFirewallFile(t, firewallPresetTCP), agentFirewallFile(t, firewallPresetTCP), func(m *e2eMesh) {
		fn := func(body map[string]any) any {
			if body["nodeId"] != "node-b" {
				return map[string]any{}
			}
			if raw, ok := body["firewall"]; ok {
				buf, _ := json.Marshal(raw)
				r := new(fwpolicy.Ack)
				json.Unmarshal(buf, r)
				report.Store(r)
			}
			if p := policy.Load(); p != nil {
				return map[string]any{"firewall": p}
			}
			return map[string]any{}
		}
		m.onHeartbeat.Store(&fn)
	})
	file := filepath.Join(m.dir, "b", "nebula.yaml")
	beat := func() {
		t.Helper()
		if err := sendHeartbeat(b); err != nil {
			t.Fatalf("b's heartbeat: %v", err)
		}
	}
	tunnel := func() uint32 {
		t.Helper()
		for _, h := range a.control().ListHostmapHosts(false) {
			if len(h.VpnAddrs) > 0 && h.VpnAddrs[0].String() == ipB {
				return h.LocalIndex
			}
		}
		t.Fatal("a has no tunnel to b")
		return 0
	}
	if udpReaches(t, a, ipB+":9") {
		t.Fatal("the TCP-only block lets UDP from a in")
	}
	start := tunnel()

	const anyFromNodes, tcpFromNodes = "in any any groups=node", "in tcp any groups=node"
	for _, c := range []struct {
		preset    string
		udp       bool
		has, hasn string
	}{
		{fwpolicy.PresetAll, true, anyFromNodes, tcpFromNodes},
		{fwpolicy.PresetTCP, false, tcpFromNodes, anyFromNodes},
	} {
		p := delivered(c.preset)
		policy.Store(&p)
		beat()
		if got := udpReaches(t, a, ipB+":9"); got != c.udp {
			t.Errorf("%s: UDP from a reaches b: %v, want %v", c.preset, got, c.udp)
		}
		// A tunnel that carries nothing drops UDP too; TCP to the agent's
		// port, which every block lets in from a node, still gets through.
		if _, succeeded, _, _ := keepaliveOneCycle(a); succeeded != 1 {
			t.Errorf("%s: TCP from a does not reach b's agent port: the tunnel carries nothing", c.preset)
		}
		rules := firewallRules(t, readFile(t, file))
		if !slices.Contains(rules, c.has) || slices.Contains(rules, c.hasn) {
			t.Errorf("%s: nebula.yaml allows %q; want %q and not %q", c.preset, rules, c.has, c.hasn)
		}
		beat()
		if r := report.Load(); r == nil || r.Fingerprint != p.Fingerprint || r.Error != "" {
			t.Errorf("%s: b reports %+v, want the policy's fingerprint %.12s", c.preset, r, p.Fingerprint)
		}
	}
	if got := tunnel(); got != start {
		t.Errorf("a's tunnel to b is a new one (index %d, was %d): a reload made a handshake", got, start)
	}
}

func delivered(preset string, rules ...fwpolicy.DeliveredRule) fwpolicy.Delivered {
	if rules == nil {
		rules = []fwpolicy.DeliveredRule{}
	}
	d := fwpolicy.Delivered{Preset: preset, Rules: rules}
	d.Fingerprint = d.Sum()
	return d
}
