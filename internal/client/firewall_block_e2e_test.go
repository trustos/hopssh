package client

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slackhq/nebula/config"
	"gopkg.in/yaml.v3"

	"github.com/trustos/hopssh/internal/fwpolicy"
)

// TestE2E_BlockIncoming: phase 3 of docs/wiki/concepts/firewall-rules.md,
// the device's own switch, through the local API, with two real Nebula
// peers. "Block incoming" leaves what hopssh always keeps (TCP 41820 from
// admin and from every node, ICMP) and nothing else, at once and without a
// new handshake; nebula.yaml keeps the network's rules, so switching it off
// brings them back. A policy from the heartbeat does not lift it, nor does
// a restart of the network, and the heartbeat says it is on.
func TestE2E_BlockIncoming(t *testing.T) {
	var policy atomic.Pointer[fwpolicy.Delivered]
	var report atomic.Pointer[fwpolicy.Ack]
	// While watch is on, the switch has been on for a while: every heartbeat
	// of b must say so, as the server takes one that doesn't to mean it is
	// off. silent counts those that don't, whoever sent them.
	var watch atomic.Bool
	var silent atomic.Int32
	m, a, b, ipB := firewallMesh(t, agentFirewallFile(t, firewallPresetAll), agentFirewallFile(t, firewallPresetAll), func(m *e2eMesh) {
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
			if watch.Load() && (r == nil || !r.Blocked) {
				silent.Add(1)
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
	srv := &localAPIServer{client: m.c, token: "test-token", events: newLocalEventHub()}
	api := localAuthMiddleware(srv.token, srv.routes())
	call := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		var r *http.Request
		if body != nil {
			raw, _ := json.Marshal(body)
			r = httptest.NewRequest(method, path, bytes.NewReader(raw))
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		r.Header.Set("Authorization", "Bearer "+srv.token)
		r.RemoteAddr = "127.0.0.1:50000"
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, r)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	block := func(on bool) {
		t.Helper()
		code, out := call("POST", "/local/block-incoming", map[string]any{"enrollment": "b", "enabled": on})
		if code != http.StatusOK || out["blockIncoming"] != on || out["applied"] != true {
			t.Fatalf("block incoming %v: %d %v", on, code, out)
		}
	}
	statusSays := func(want bool) {
		t.Helper()
		_, out := call("GET", "/local/status", nil)
		es, _ := out["enrollments"].([]any)
		for _, e := range es {
			if e, _ := e.(map[string]any); e["name"] == "b" {
				if got, _ := e["blockIncoming"].(bool); got != want {
					t.Errorf("/local/status says blockIncoming %v for b; want %v", got, want)
				}
				return
			}
		}
		t.Errorf("/local/status has no b: %v", out)
	}
	live := func() *meshInstance {
		t.Helper()
		inst := m.c.instances.get("b")
		if inst == nil {
			t.Fatal("b is not up")
		}
		return inst
	}
	// running: what the firewall b runs allows, from the text Nebula loaded.
	running := func() []string {
		t.Helper()
		ip, ok := live().liveSvc().(inPlaceConfig)
		if !ok || ip.loaded() == nil {
			t.Fatal("b's mesh has no loaded config")
		}
		return firewallRules(t, ip.loaded().loadedText())
	}
	beat := func() {
		t.Helper()
		if err := sendHeartbeat(live()); err != nil {
			t.Fatalf("b's heartbeat: %v", err)
		}
	}
	// beatUntil: a few beats, for a policy that a beat the switch triggered
	// had too (one of them waits for the next).
	beatUntil := func(what string, ok func() bool) {
		t.Helper()
		for i := 0; i < 5; i++ {
			beat()
			if ok() {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Errorf("after 5 heartbeats, %s", what)
	}
	reported := func(fingerprint string, blocked bool) {
		t.Helper()
		beat()
		r := report.Load()
		if r == nil || r.Fingerprint != fingerprint || r.Blocked != blocked || r.Error != "" {
			t.Errorf("b reports %+v; want fingerprint %.12s, blocked %v", r, fingerprint, blocked)
		}
	}
	dial := func(port string) bool {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		c, err := a.currentSvc().(meshDialer).DialContext(ctx, "tcp", ipB+":"+port)
		if err == nil {
			c.Close()
		}
		return err == nil
	}
	keepalive := func(when string) {
		t.Helper()
		if _, succeeded, _, _ := keepaliveOneCycle(a); succeeded != 1 {
			t.Errorf("%s: a's keepalive does not reach b (succeeded %d)", when, succeeded)
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
	// serve: a listener on TCP 7000 of b's mesh; a restart closes it.
	serve := func() {
		t.Helper()
		ln, err := live().currentSvc().Listen("tcp", ":7000")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				c.Close()
			}
		}()
	}
	restart := func() {
		t.Helper()
		if err := m.c.Disconnect(context.Background(), "b"); err != nil {
			t.Fatal(err)
		}
		if err := m.c.Connect(context.Background(), "b"); err != nil {
			t.Fatal(err)
		}
		serve()
	}
	_ = b
	serve()
	always := firewallRules(t, agentFirewallYAML(firewallPresetPing))
	unblocked := running()

	// The preset b joined with: everything from every node.
	if !udpReaches(t, a, ipB+":9") || !dial("7000") {
		t.Fatal("the all block does not let a in")
	}
	beat()
	if r := report.Load(); r != nil {
		t.Errorf("b reports %+v before any policy or switch", r)
	}
	statusSays(false)
	start := tunnel()

	// On: only what hopssh keeps, the file as it was.
	before := readFile(t, file)
	block(true)
	// The dashboard hears it at once: the switch sends a heartbeat of its
	// own; the next one of the loop is a minute away.
	within(t, 3*time.Second, "a heartbeat that says b blocks incoming", func() bool {
		r := report.Load()
		return r != nil && r.Blocked
	})
	if got := running(); !slices.Equal(got, always) {
		t.Errorf("b blocks incoming and runs\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(always, "\n  "))
	}
	if udpReaches(t, a, ipB+":9") {
		t.Error("UDP from a reaches b while it blocks incoming")
	}
	if dial("7000") {
		t.Error("TCP 7000 from a reaches b while it blocks incoming")
	}
	keepalive("blocked")
	if readFile(t, file) != before {
		t.Error("blocking incoming changed nebula.yaml")
	}
	statusSays(true)
	reported("", true)
	block(true) // again: nothing changes
	if got := running(); !slices.Equal(got, always) {
		t.Errorf("a second block: b runs %q", got)
	}

	// A policy comes while it is on: nebula.yaml takes it, Nebula does not.
	p := delivered(fwpolicy.PresetPing,
		fwpolicy.DeliveredRule{Proto: "udp", Port: "9", Source: ipA},
		fwpolicy.DeliveredRule{Proto: "tcp", Port: "7000"})
	policy.Store(&p)
	beatUntil("a policy that came while b blocks incoming is not in nebula.yaml", func() bool {
		return slices.Contains(firewallRules(t, readFile(t, file)), "in udp 9 cidr="+ipA+"/32")
	})
	if got := running(); !slices.Equal(got, always) {
		t.Errorf("a policy lifted the block: b runs %q", got)
	}
	if udpReaches(t, a, ipB+":9") || dial("7000") {
		t.Error("a policy lifted the block: a reaches b")
	}
	reported(p.Fingerprint, true)
	if got := tunnel(); got != start {
		t.Errorf("a's tunnel to b is a new one (index %d, was %d)", got, start)
	}

	// heard: no heartbeat of b said the switch was off while it was on.
	heard := func(when string) {
		t.Helper()
		watch.Store(false)
		if n := silent.Swap(0); n != 0 {
			t.Errorf("%s: %d heartbeats of b did not say it blocks incoming", when, n)
		}
	}

	// A restart of the network: still on.
	watch.Store(true)
	restart()
	if got := running(); !slices.Equal(got, always) {
		t.Errorf("after a restart b runs %q; it blocks incoming", got)
	}
	if udpReaches(t, a, ipB+":9") || dial("7000") {
		t.Error("after a restart a reaches b, which blocks incoming")
	}
	keepalive("blocked, after a restart")
	// The policy is checked again at the first beat after the start; every
	// report says the switch is on.
	beatUntil("b does not report the policy after a restart", func() bool {
		r := report.Load()
		if r == nil || !r.Blocked {
			t.Errorf("after a restart b reports %+v; it blocks incoming", r)
			return true
		}
		return r.Fingerprint == p.Fingerprint
	})
	if udpReaches(t, a, ipB+":9") {
		t.Error("the policy after a restart lifted the block")
	}
	heard("a restart while blocked")

	// Off, while nebula.yaml has rules Nebula refuses: the switch is off from
	// now on (a start would load those rules, or fail), Nebula keeps blocking,
	// and the report says what Nebula runs, not what the switch says.
	{
		good := readFile(t, file)
		cfg, err := parseNebulaYAML([]byte(good))
		if err != nil {
			t.Fatal(err)
		}
		cfg["firewall"] = map[string]any{
			"outbound": []any{map[string]any{"port": "any", "proto": "any", "host": "any"}},
			"inbound":  []any{map[string]any{"port": "nope", "proto": "tcp", "host": "any"}},
		}
		bad, err := yaml.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, bad, 0o644); err != nil {
			t.Fatal(err)
		}
		code, out := call("POST", "/local/block-incoming", map[string]any{"enrollment": "b", "enabled": false})
		if code != http.StatusOK || out["blockIncoming"] != false || out["applied"] != false || out["error"] == nil {
			t.Errorf("switching off to rules Nebula refuses: %d %v; want applied false and why", code, out)
		}
		if got := running(); !slices.Equal(got, always) {
			t.Errorf("Nebula refused the rules, yet b runs %q", got)
		}
		statusSays(false) // the switch
		reported(p.Fingerprint, true)
		if err := os.WriteFile(file, []byte(good), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Off: the policy runs.
	block(false)
	within(t, 3*time.Second, "a heartbeat that says b no longer blocks incoming", func() bool {
		r := report.Load()
		return r != nil && !r.Blocked
	})
	if !udpReaches(t, a, ipB+":9") || !dial("7000") {
		t.Error("switched off, the policy does not let a in")
	}
	if got := running(); !slices.Contains(got, "in udp 9 cidr="+ipA+"/32") || slices.Equal(got, unblocked) {
		t.Errorf("switched off, b runs %q", got)
	}
	statusSays(false)
	reported(p.Fingerprint, false)
	keepalive("unblocked")

	// Switched on while the network is down: it comes up blocked.
	if err := m.c.Disconnect(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	code, out := call("POST", "/local/block-incoming", map[string]any{"enrollment": "b", "enabled": true})
	if code != http.StatusOK || out["blockIncoming"] != true || out["applied"] != false {
		t.Fatalf("block incoming while down: %d %v; want applied false", code, out)
	}
	statusSays(true)
	watch.Store(true)
	if err := m.c.Connect(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	serve()
	if got := running(); !slices.Equal(got, always) {
		t.Errorf("switched on while down, b comes up with %q", got)
	}
	beat()
	heard("a start after the switch went on while down")
	block(false)
	if got := running(); !slices.Contains(got, "in udp 9 cidr="+ipA+"/32") {
		t.Errorf("switched off again, b runs %q", got)
	}

	// Requests it refuses.
	for name, c := range map[string]struct {
		body any
		code int
	}{
		"no enrollment":      {map[string]any{"enabled": true}, http.StatusBadRequest},
		"unknown enrollment": {map[string]any{"enrollment": "nope", "enabled": true}, http.StatusNotFound},
		"no enabled":         {map[string]any{"enrollment": "b"}, http.StatusBadRequest},
		"an unknown field":   {map[string]any{"enrollment": "b", "enabled": true, "evrything": true}, http.StatusBadRequest},
	} {
		if code, out := call("POST", "/local/block-incoming", c.body); code != c.code {
			t.Errorf("%s: %d %v; want %d", name, code, out, c.code)
		}
	}
	if got := running(); slices.Equal(got, always) {
		t.Error("a refused request blocked incoming")
	}
	if !udpReaches(t, a, ipB+":9") {
		t.Error("after the refused requests a does not reach b")
	}
}

// TestE2E_BlockIncoming_AfterBusyReload: the switch while an earlier
// in-place reload has not finished (one past its 5 s limit that still
// returns) is saved and refused, as one reload runs at a time. The next
// heartbeat gives it to Nebula; nothing else would before Nebula's next
// start. On, and then off.
func TestE2E_BlockIncoming_AfterBusyReload(t *testing.T) {
	_, a, b, ipB := firewallMesh(t, agentFirewallFile(t, firewallPresetAll), agentFirewallFile(t, firewallPresetAll), nil)
	conf := b.liveSvc().(inPlaceConfig).loaded()
	// hold, when set, keeps a reload of b's Nebula from returning; it runs
	// after Nebula's own callbacks.
	var hold atomic.Pointer[chan struct{}]
	conf.cfg.RegisterReloadCallback(func(*config.C) {
		if c := hold.Load(); c != nil {
			<-*c
		}
	})
	// busy: a reload of what b runs that returns only after release.
	busy := func() (release func()) {
		t.Helper()
		c := make(chan struct{})
		hold.Store(&c)
		if err := reloadInPlace(b, conf, conf.loadedText(), conf.loadedText(), conf.isBlocked()); err == nil {
			t.Fatal("a held reload returned within its limit")
		}
		return func() {
			t.Helper()
			hold.Store(nil)
			close(c)
			within(t, 5*time.Second, "the late reload to return", func() bool {
				conf.mu.Lock()
				defer conf.mu.Unlock()
				return !conf.pending
			})
		}
	}
	for _, on := range []bool{true, false} {
		release := busy()
		if err := setIncomingBlocked(b, on); err == nil || !strings.Contains(err.Error(), "earlier reload") {
			t.Fatalf("block incoming %v while a reload runs: %v; want it refused", on, err)
		}
		if incomingBlocked(b.dir()) != on {
			t.Fatalf("block incoming %v while a reload runs: not saved", on)
		}
		release()
		if err := sendHeartbeat(b); err != nil {
			t.Fatalf("b's heartbeat: %v", err)
		}
		if got := conf.isBlocked(); got != on {
			t.Errorf("after the heartbeat, b's Nebula blocks incoming: %v; the switch says %v", got, on)
		}
		if reaches := udpReaches(t, a, ipB+":9"); reaches == on {
			t.Errorf("block incoming %v, after the heartbeat: UDP from a reaches b: %v", on, reaches)
		}
	}
}
