package client

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slackhq/nebula/cert"
	"golang.org/x/crypto/curve25519"

	"github.com/trustos/hopssh/internal/blocklist"
)

// docs/wiki/concepts/deleted-devices.md, the device's side: the list from
// the heartbeat is kept in blocklist.json and given to Nebula as
// pki.blocklist, at once and at every start.

// writeTestNodeCert writes a node.crt signed by a throwaway CA into dir and
// returns its fingerprint.
func writeTestNodeCert(t *testing.T, dir string) string {
	t.Helper()
	caPub, caPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca, err := (&cert.TBSCertificate{Version: cert.Version1, Name: "ca", IsCA: true, PublicKey: caPub, Curve: cert.Curve_CURVE25519,
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(48 * time.Hour)}).Sign(nil, cert.Curve_CURVE25519, caPriv)
	if err != nil {
		t.Fatal(err)
	}
	k := make([]byte, 32)
	rand.Read(k)
	pub, err := curve25519.X25519(k, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	c, err := (&cert.TBSCertificate{Version: cert.Version1, Name: "node", PublicKey: pub, Curve: cert.Curve_CURVE25519,
		Networks: []netip.Prefix{netip.MustParsePrefix("10.42.1.6/24")}, Groups: []string{"node"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour)}).Sign(ca, cert.Curve_CURVE25519, caPriv)
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.MarshalPEM()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node.crt"), p, 0o644); err != nil {
		t.Fatal(err)
	}
	fp, _, err := blocklist.Fingerprint(p)
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

// fp is a fingerprint-shaped string, distinct per n.
func fp(n byte) string { return strings.Repeat(string("0123456789abcdef"[n%16]), 64) }

func writeBlocklistFile(t *testing.T, dir string, v any) {
	t.Helper()
	raw, ok := v.(string)
	if !ok {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		raw = string(b)
	}
	if err := os.WriteFile(filepath.Join(dir, blocklistFile), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadBlocklist(t *testing.T) {
	until := time.Now().Add(time.Hour).Unix()
	good := []blocklist.Entry{{Fingerprint: fp(1), Until: until}}
	for name, tc := range map[string]struct {
		file any // nil: no file
		want []blocklist.Entry
	}{
		"missing":         {nil, nil},
		"not JSON":        {"{nope", nil},
		"upper case":      {[]blocklist.Entry{{Fingerprint: strings.ToUpper(fp(10)), Until: until}}, nil},
		"no until":        {[]blocklist.Entry{{Fingerprint: fp(1)}}, nil},
		"too long":        {make([]blocklist.Entry, blocklist.MaxEntries+1), nil},
		"valid":           {good, good},
		"valid and empty": {"[]", []blocklist.Entry{}},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.file != nil {
				writeBlocklistFile(t, dir, tc.file)
			}
			got := loadBlocklist(dir)
			if !slices.Equal(got, tc.want) || (got == nil) != (tc.want == nil) {
				t.Errorf("loadBlocklist = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestLoadNebulaConfig_Blocklist: a start gives Nebula the saved entries
// that still need blocking, never the device's own certificate, and starts
// without a list it can't use.
func TestLoadNebulaConfig_Blocklist(t *testing.T) {
	now := time.Now()
	setup := func(t *testing.T, file any, blocked bool) (string, string) {
		t.Helper()
		dir := filepath.Dir(agentFirewallFile(t, firewallPresetAll))
		own := writeTestNodeCert(t, dir)
		if file != nil {
			if l, ok := file.([]blocklist.Entry); ok {
				for i := range l {
					if l[i].Fingerprint == "own" {
						l[i].Fingerprint = own
					}
				}
			}
			writeBlocklistFile(t, dir, file)
		}
		if blocked {
			if err := saveIncomingBlocked(dir, true); err != nil {
				t.Fatal(err)
			}
		}
		return filepath.Join(dir, "nebula.yaml"), own
	}
	running := func(t *testing.T, path string) (fps []string, firewall []string) {
		t.Helper()
		conf, err := loadNebulaConfig(path)
		if err != nil {
			t.Fatalf("loadNebulaConfig: %v", err)
		}
		cfg, err := parseNebulaYAML([]byte(conf.loadedText()))
		if err != nil {
			t.Fatal(err)
		}
		if got := conf.cfg.GetStringSlice("pki.blocklist", nil); !slices.Equal(got, configBlocklist(cfg)) {
			t.Errorf("Nebula's config has pki.blocklist %v, the text %v", got, configBlocklist(cfg))
		}
		return configBlocklist(cfg), firewallRules(t, conf.loadedText())
	}

	t.Run("active, expired and own", func(t *testing.T) {
		path, _ := setup(t, []blocklist.Entry{
			{Fingerprint: fp(2), Until: now.Add(time.Hour).Unix()},
			{Fingerprint: fp(1), Until: now.Add(time.Hour).Unix()},
			{Fingerprint: fp(1), Until: now.Add(2 * time.Hour).Unix()},
			{Fingerprint: fp(3), Until: now.Add(-time.Second).Unix()},
			{Fingerprint: "own", Until: now.Add(time.Hour).Unix()},
		}, false)
		before := readFile(t, path)
		got, _ := running(t, path)
		if want := []string{fp(1), fp(2)}; !slices.Equal(got, want) {
			t.Errorf("pki.blocklist %v, want %v", got, want)
		}
		if readFile(t, path) != before {
			t.Error("loading the config changed nebula.yaml")
		}
	})
	t.Run("only own and expired", func(t *testing.T) {
		path, _ := setup(t, []blocklist.Entry{
			{Fingerprint: "own", Until: now.Add(time.Hour).Unix()},
			{Fingerprint: fp(3), Until: now.Add(-time.Second).Unix()},
		}, false)
		if got, _ := running(t, path); got != nil {
			t.Errorf("pki.blocklist %v, want none", got)
		}
	})
	t.Run("a bad file still starts", func(t *testing.T) {
		path, _ := setup(t, "[{]", false)
		if got, _ := running(t, path); got != nil {
			t.Errorf("pki.blocklist %v, want none", got)
		}
	})
	t.Run("blocked too", func(t *testing.T) {
		path, _ := setup(t, []blocklist.Entry{{Fingerprint: fp(4), Until: now.Add(time.Hour).Unix()}}, true)
		got, fw := running(t, path)
		if want := []string{fp(4)}; !slices.Equal(got, want) {
			t.Errorf("pki.blocklist %v, want %v", got, want)
		}
		if want := firewallRules(t, agentFirewallYAML(firewallPresetPing)); !slices.Equal(fw, want) {
			t.Errorf("blocked with a list, the firewall is %v, want %v", fw, want)
		}
	})
}

func TestSameApartFromOverlays(t *testing.T) {
	text := readFile(t, agentFirewallFile(t, firewallPresetAll))
	base := func() map[string]interface{} {
		cfg, err := parseNebulaYAML([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	a, b := base(), base()
	if err := setBlocklist(b, []string{fp(1)}); err != nil {
		t.Fatal(err)
	}
	b["firewall"] = "other"
	if !sameApartFromOverlays(a, b) || !sameApartFromOverlays(b, a) {
		t.Error("configs that differ in pki.blocklist and the firewall count as different")
	}
	b["pki"].(map[string]interface{})["cert"] = "/elsewhere/node.crt"
	if sameApartFromOverlays(a, b) {
		t.Error("configs that differ in pki.cert count as the same")
	}
}

// TestTakeBlocklist_Saves: what a heartbeat answer leaves in blocklist.json
// when no Nebula runs.
func TestTakeBlocklist_Saves(t *testing.T) {
	until := time.Now().Add(time.Hour).Unix()
	list := []blocklist.Entry{{Fingerprint: fp(1), Until: until}}
	listJSON, _ := json.Marshal(list)
	newInst := func(t *testing.T) (*meshInstance, string) {
		inst := newMeshInstance(&Enrollment{Name: "home"})
		inst.customDir = t.TempDir()
		return inst, filepath.Join(inst.customDir, blocklistFile)
	}
	for _, raw := range []string{"", "null"} {
		inst, path := newInst(t)
		takeBlocklist(inst, json.RawMessage(raw), false)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("answer %q wrote a list (%v)", raw, err)
		}
	}
	for _, raw := range []string{`"x"`, `{}`, `[{"fingerprint":"` + strings.ToUpper(fp(10)) + `","until":1}]`, `[{"fingerprint":"` + fp(1) + `"}]`} {
		inst, path := newInst(t)
		takeBlocklist(inst, listJSON, false)
		takeBlocklist(inst, json.RawMessage(raw), false)
		if got := readFile(t, path); got != string(listJSON) {
			t.Errorf("after the answer %s the list is %s, want %s", raw, got, listJSON)
		}
	}

	inst, path := newInst(t)
	takeBlocklist(inst, listJSON, false)
	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	takeBlocklist(inst, listJSON, false)
	if again, err := os.Stat(path); err != nil || !os.SameFile(first, again) {
		t.Errorf("the same list was written again (%v)", err)
	}
	// A new instance, as after a restart, reads what is there first.
	inst2 := newMeshInstance(&Enrollment{Name: "home"})
	inst2.customDir = inst.customDir
	takeBlocklist(inst2, listJSON, false)
	if again, err := os.Stat(path); err != nil || !os.SameFile(first, again) {
		t.Errorf("after a restart the same list was written again (%v)", err)
	}
	takeBlocklist(inst, json.RawMessage("[]"), false)
	if got := readFile(t, path); got != "[]" {
		t.Errorf("an empty list from the server left %s", got)
	}
}

// TestE2E_Blocklist: two real Nebula peers. b's certificate in a heartbeat
// answer of a: a cuts the tunnel and refuses b both ways, and goes on
// applying the firewall from the heartbeat; a list it refuses or one that is
// absent changes nothing; a restart keeps b out from the saved list, and a
// start with nothing saved refuses b before its warm-up ends. Once the entry
// has expired or is gone, b comes back.
func TestE2E_Blocklist(t *testing.T) {
	var answer atomic.Pointer[map[string]any]
	// warmUp: answers only the heartbeat a start sends before its warm-up
	// (it has no agentVersion), the one with the list.
	var warmUp atomic.Pointer[map[string]any]
	m, _, b, ipB := firewallMesh(t, agentFirewallFile(t, firewallPresetAll), agentFirewallFile(t, firewallPresetAll), func(m *e2eMesh) {
		fn := func(body map[string]any) any {
			if body["nodeId"] != "node-a" {
				return map[string]any{}
			}
			if w := warmUp.Load(); w != nil && body["agentVersion"] == nil {
				return *w
			}
			if p := answer.Load(); p != nil {
				return *p
			}
			return map[string]any{}
		}
		m.onHeartbeat.Store(&fn)
	})
	ipA := e2eSubnet + "6"
	live := func() *meshInstance {
		t.Helper()
		inst := m.c.instances.get("a")
		if inst == nil {
			t.Fatal("a is not up")
		}
		return inst
	}
	fpB, _, err := blocklist.Fingerprint([]byte(readFile(t, filepath.Join(b.dir(), "node.crt"))))
	if err != nil {
		t.Fatal(err)
	}
	fpA, _, err := blocklist.Fingerprint([]byte(readFile(t, filepath.Join(live().dir(), "node.crt"))))
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Hour).Unix()
	entry := func(fp string, until int64) map[string]any { return map[string]any{"fingerprint": fp, "until": until} }
	beat := func(ans map[string]any) {
		t.Helper()
		answer.Store(&ans)
		if err := sendHeartbeat(live()); err != nil {
			t.Fatalf("a's heartbeat: %v", err)
		}
	}
	loaded := func() *loadedConfig {
		t.Helper()
		ip, ok := live().liveSvc().(inPlaceConfig)
		if !ok || ip.loaded() == nil {
			t.Fatal("a's mesh has no loaded config")
		}
		return ip.loaded()
	}
	running := func() []string {
		t.Helper()
		cfg, err := parseNebulaYAML([]byte(loaded().loadedText()))
		if err != nil {
			t.Fatal(err)
		}
		return configBlocklist(cfg)
	}
	// pool: what a's Nebula refuses, which the text it loaded need not be.
	pool := func() []string { t.Helper(); return live().control().CABlocklist() }
	caFile := func() string {
		t.Helper()
		cfg, err := parseNebulaYAML([]byte(loaded().loadedText()))
		if err != nil {
			t.Fatal(err)
		}
		pki, _ := cfg["pki"].(map[string]interface{})
		ca, _ := pki["ca"].(string)
		if ca == "" || strings.Contains(ca, "-----BEGIN") {
			t.Fatalf("a's pki.ca is %q, want a path", ca)
		}
		return ca
	}
	tunnel := func() uint32 {
		t.Helper()
		for _, h := range live().control().ListHostmapHosts(false) {
			if len(h.VpnAddrs) > 0 && h.VpnAddrs[0].String() == ipB {
				return h.LocalIndex
			}
		}
		return 0
	}
	saved := filepath.Join(live().dir(), blocklistFile)
	yamlPath := filepath.Join(live().dir(), "nebula.yaml")
	yamlBefore := readFile(t, yamlPath)
	refused := func(when string) {
		t.Helper()
		for i := 0; i < 3; i++ {
			if udpReaches(t, b, ipA+":9") {
				t.Errorf("%s: b reaches a (try %d)", when, i)
			}
			if udpReaches(t, live(), ipB+":9") {
				t.Errorf("%s: a reaches b (try %d)", when, i)
			}
		}
	}
	back := func(when string) {
		t.Helper()
		within(t, 30*time.Second, when+": b reaches a again", func() bool { return udpReaches(t, b, ipA+":9") })
	}
	fwRules := func() []string { t.Helper(); return firewallRules(t, loaded().loadedText()) }
	allRules := fwRules()

	start := tunnel()
	if start == 0 || !udpReaches(t, live(), ipB+":9") {
		t.Fatal("a does not reach b")
	}

	// A list without b: b stays.
	beat(map[string]any{"blocklist": []any{entry(fp(1), until)}})
	if got := running(); !slices.Equal(got, []string{fp(1)}) {
		t.Fatalf("a runs pki.blocklist %v, want [%s]", got, fp(1))
	}
	if got := pool(); !slices.Equal(got, []string{fp(1)}) {
		t.Fatalf("a's CA pool refuses %v, want [%s]", got, fp(1))
	}
	if got := tunnel(); got != start {
		t.Errorf("a list without b changed the tunnel to b: %d -> %d", start, got)
	}
	if !udpReaches(t, live(), ipB+":9") {
		t.Error("a list without b: a does not reach b")
	}

	// The CA can't be read while a reloads: Nebula keeps its old pool and
	// only logs. The list is not in force, and the next beat tries again.
	withB := map[string]any{"blocklist": []any{entry(fp(1), until), entry(fpB, until), entry(fpA, until)}}
	ca := caFile()
	if err := os.Rename(ca, ca+".away"); err != nil {
		t.Fatal(err)
	}
	beat(withB)
	if err := os.Rename(ca+".away", ca); err != nil {
		t.Fatal(err)
	}
	if got := pool(); !slices.Equal(got, []string{fp(1)}) {
		t.Errorf("with its CA unreadable a's pool refuses %v, want [%s]", got, fp(1))
	}

	// b and a's own certificate: b is cut off, a keeps its own.
	t0 := time.Now()
	beat(withB)
	// Sorted: b's fingerprint is random, and sorts before fp(1) about one
	// time in 16.
	oneAndB := []string{fp(1), fpB}
	slices.Sort(oneAndB)
	if got := running(); !slices.Equal(got, oneAndB) {
		t.Fatalf("a runs pki.blocklist %v, want %v", got, oneAndB)
	}
	if got := pool(); !slices.Equal(got, oneAndB) {
		t.Fatalf("a's CA pool refuses %v, want %v", got, oneAndB)
	}
	within(t, 20*time.Second, "a's tunnel to b to end", func() bool { return tunnel() == 0 })
	t.Logf("the tunnel to b ended %v after the heartbeat", time.Since(t0).Round(100*time.Millisecond))
	refused("b blocked")
	if readFile(t, yamlPath) != yamlBefore {
		t.Error("the list was written into nebula.yaml")
	}

	// A firewall policy while b is blocked, with no list or a null one: the
	// policy applies and the list stays.
	beat(map[string]any{"firewall": delivered(firewallPresetPing)})
	if got, want := fwRules(), firewallRules(t, agentFirewallYAML(firewallPresetPing)); !slices.Equal(got, want) {
		t.Errorf("a policy while b is blocked: a runs %v, want %v", got, want)
	}
	if got := running(); !slices.Contains(got, fpB) {
		t.Errorf("a firewall policy dropped the list: %v", got)
	}
	beat(map[string]any{"firewall": delivered(firewallPresetAll), "blocklist": nil})
	if got := fwRules(); !slices.Equal(got, allRules) {
		t.Errorf("a policy with a null list: a runs %v, want %v", got, allRules)
	}
	if got := running(); !slices.Contains(got, fpB) {
		t.Errorf("a null list dropped the list: %v", got)
	}

	// Lists a refuses, with a policy in the same answer: the policy applies,
	// nothing else changes.
	savedBefore := readFile(t, saved)
	for i, bad := range []any{"nonsense", []any{entry(strings.ToUpper(fpB), until)}, []any{entry(fpB, 0)}} {
		preset := []string{firewallPresetPing, firewallPresetAll}[i%2]
		beat(map[string]any{"firewall": delivered(preset), "blocklist": bad})
		if got, want := fwRules(), firewallRules(t, agentFirewallYAML(preset)); !slices.Equal(got, want) {
			t.Errorf("with the bad list %v, the policy %s did not apply: a runs %v", bad, preset, got)
		}
		if got := running(); !slices.Contains(got, fpB) {
			t.Errorf("the bad list %v changed what a runs: %v", bad, got)
		}
		if got := readFile(t, saved); got != savedBefore {
			t.Errorf("the bad list %v was saved: %s", bad, got)
		}
	}
	beat(map[string]any{"firewall": delivered(firewallPresetAll)})
	if got := fwRules(); !slices.Equal(got, allRules) {
		t.Fatalf("a runs %v, want %v", got, allRules)
	}
	refused("after the policies")

	// A restart with a server that sends no list: the saved one applies.
	restart := func() {
		t.Helper()
		if err := m.c.Disconnect(context.Background(), "a"); err != nil {
			t.Fatal(err)
		}
		if err := m.c.Connect(context.Background(), "a"); err != nil {
			t.Fatal(err)
		}
	}
	answer.Store(&map[string]any{})
	restart()
	if got := running(); !slices.Contains(got, fpB) {
		t.Errorf("after a restart a runs pki.blocklist %v; b is not in it", got)
	}
	refused("after a restart")

	// A renewal's hot restart, and a heartbeat that saves a new list after
	// the new Nebula read the file: the restart gives it that list, without
	// waiting for the next beat.
	inst := live()
	withTwo := []blocklist.Entry{{Fingerprint: fp(1), Until: until}, {Fingerprint: fpB, Until: until}, {Fingerprint: fp(2), Until: until}}
	prevStart := startNebulaByModeFn
	startNebulaByModeFn = func(configPath, tunMode string) (meshService, error) {
		svc, err := prevStart(configPath, tunMode)
		if err == nil {
			if serr := saveBlocklist(inst, withTwo); serr != nil {
				t.Error(serr)
			}
		}
		return svc, err
	}
	reloadNebula(inst)
	startNebulaByModeFn = prevStart
	want := []string{fp(1), fpB, fp(2)}
	slices.Sort(want)
	if got := pool(); !slices.Equal(got, want) {
		t.Errorf("after a hot restart a's CA pool refuses %v, want %v", got, want)
	}

	// The same when the restart's start fails and the retry starts Nebula.
	withThree := append(slices.Clone(withTwo), blocklist.Entry{Fingerprint: fp(3), Until: until})
	prevSchedule := retryReloadBackoffSchedule()
	short := []time.Duration{100 * time.Millisecond}
	retryReloadBackoff.Store(&short)
	var starts atomic.Int32
	startNebulaByModeFn = func(configPath, tunMode string) (meshService, error) {
		if starts.Add(1) == 1 {
			return nil, errors.New("the UDP port is still held (test)")
		}
		svc, err := prevStart(configPath, tunMode)
		if err == nil {
			if serr := saveBlocklist(inst, withThree); serr != nil {
				t.Error(serr)
			}
		}
		return svc, err
	}
	reloadNebula(inst)
	want = append(want, fp(3))
	slices.Sort(want)
	// setSvc comes before the list, under startMu: wait for the pool.
	settled := func() bool {
		svc := inst.liveSvc()
		return svc != nil && svc.NebulaControl() != nil && slices.Equal(svc.NebulaControl().CABlocklist(), want)
	}
	deadline := time.Now().Add(15 * time.Second)
	for !settled() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !settled() {
		got := []string(nil)
		if svc := inst.liveSvc(); svc != nil && svc.NebulaControl() != nil {
			got = svc.NebulaControl().CABlocklist()
		}
		t.Errorf("after a retried restart a's CA pool refuses %v (%d starts), want %v", got, starts.Load(), want)
	}
	inst.startMu.Lock() // the retry has returned: nothing reads the two below
	startNebulaByModeFn = prevStart
	retryReloadBackoff.Store(&prevSchedule)
	inst.startMu.Unlock()
	if inst.liveSvc() == nil {
		t.Fatal("a's Nebula did not come back after the retry")
	}

	// A start with nothing saved: the warm-up heartbeat brings the list,
	// before the start ends.
	if err := m.c.Disconnect(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(saved); err != nil {
		t.Fatal(err)
	}
	warmUp.Store(&map[string]any{"blocklist": []any{entry(fpB, until)}})
	if err := m.c.Connect(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	warmUp.Store(nil)
	if got := running(); !slices.Equal(got, []string{fpB}) {
		t.Errorf("after a start with nothing saved a runs pki.blocklist %v, want [%s]", got, fpB)
	}
	refused("after a start with nothing saved")

	// b's entry expired: it is not given to Nebula, and b comes back.
	beat(map[string]any{"blocklist": []any{entry(fpB, time.Now().Add(-time.Second).Unix())}})
	if got := running(); got != nil {
		t.Errorf("with b's entry expired a runs pki.blocklist %v", got)
	}
	back("b's entry expired")

	// Blocked again, then the server's list is empty: b comes back.
	beat(map[string]any{"blocklist": []any{entry(fpB, until)}})
	within(t, 20*time.Second, "a's tunnel to b to end again", func() bool { return tunnel() == 0 })
	refused("blocked again")
	beat(map[string]any{"blocklist": []any{}})
	if got := running(); got != nil {
		t.Errorf("with an empty list a runs pki.blocklist %v", got)
	}
	if got := readFile(t, saved); got != "[]" {
		t.Errorf("an empty list from the server saved %s", got)
	}
	back("an empty list")
}

// configBlocklist is the pki.blocklist of cfg, sorted.
func configBlocklist(cfg map[string]interface{}) []string {
	pki, _ := cfg["pki"].(map[string]interface{})
	l, _ := pki["blocklist"].([]interface{})
	var out []string
	for _, v := range l {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out
}
