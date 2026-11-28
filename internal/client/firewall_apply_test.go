package client

import (
	"log/slog"
	"net/netip"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slackhq/nebula/config"

	"gopkg.in/yaml.v3"

	"github.com/trustos/hopssh/internal/fwpolicy"
)

// TestManagedFirewall: the block a policy renders, as Nebula's parser reads
// it. Each preset keeps what the agent always keeps (TCP 41820 from admin and
// from every node, ICMP, drop for the rest), and each rule adds one entry, by
// group node or by the source's /32, never by admin.
func TestManagedFirewall(t *testing.T) {
	rules := []fwpolicy.DeliveredRule{
		{Proto: "udp", Port: "9", Source: "198.18.77.6"},
		{Proto: "tcp", Port: "7000"},
		{Proto: "tcp", Port: "8000-8010", Source: "198.18.77.8"},
		{Proto: "any", Port: "any"},
	}
	fromRules := []string{"in udp 9 cidr=198.18.77.6/32", "in tcp 7000 groups=node", "in tcp 8000-8010 cidr=198.18.77.8/32", "in any any groups=node"}
	always := []string{"in tcp 41820 groups=admin", "in icmp any host=any", "in default drop", "out any any host=any", "out default drop"}
	for preset, own := range map[string][]string{
		fwpolicy.PresetPing: {"in tcp 41820 groups=node"},
		fwpolicy.PresetTCP:  {"in tcp any groups=node"},
		fwpolicy.PresetAll:  {"in any any groups=node"},
	} {
		d := delivered(preset, rules...)
		if err := d.Check(netip.MustParsePrefix("198.18.77.0/24")); err != nil {
			t.Fatalf("%s: the test's policy is refused: %v", preset, err)
		}
		block, err := managedFirewall(d)
		if err != nil {
			t.Fatalf("%s: %v", preset, err)
		}
		out, err := yaml.Marshal(map[string]interface{}{"firewall": block})
		if err != nil {
			t.Fatal(err)
		}
		got := firewallRules(t, string(out))
		want := sorted(slices.Concat(always, own, fromRules))
		if !slices.Equal(got, want) {
			t.Errorf("%s renders\n%q\nwant\n%q", preset, got, want)
		}
	}
}

// TestManagedFirewall_ComparesWithTheFile: the rendered block equals what a
// parse of the file it was written to gives, so a policy already in the file
// is not written again.
func TestManagedFirewall_ComparesWithTheFile(t *testing.T) {
	block, err := managedFirewall(delivered(fwpolicy.PresetPing, fwpolicy.DeliveredRule{Proto: "tcp", Port: "22"}))
	if err != nil {
		t.Fatal(err)
	}
	out, err := yaml.Marshal(map[string]interface{}{"firewall": block})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := parseNebulaYAML(out)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg["firewall"], block) {
		t.Errorf("the block read back from the file is another one:\n%#v\n%#v", cfg["firewall"], block)
	}
}

// TestReloadInPlace_OneAtATime: a reload that takes longer than its limit
// goes on alone. Until it returns no other one starts, and once it has, one
// built from the text before it is refused: it would undo it (a heartbeat
// that read the config, then the switch "Block incoming", then the beat's
// reload). Review of item 14, 2026-11-28.
func TestReloadInPlace_OneAtATime(t *testing.T) {
	const t0, t1, t2 = "a: 0\n", "a: 1\n", "a: 2\n"
	c := config.NewC(slog.New(slog.DiscardHandler))
	if err := c.LoadString(t0); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var calls atomic.Int32
	c.RegisterReloadCallback(func(*config.C) {
		if calls.Add(1) == 1 {
			<-release
		}
	})
	conf := &loadedConfig{cfg: c, text: t0}
	inst := newMeshInstance(&Enrollment{Name: "home"})

	if err := reloadInPlace(inst, conf, t0, t1, false); err == nil {
		t.Fatal("a reload that did not return within its limit reported success")
	}
	start := time.Now()
	if err := reloadInPlace(inst, conf, t0, t2, true); err == nil {
		t.Error("a second reload was taken while the first had not returned")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("the second reload was refused after %v; it should not wait", d)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("Nebula's reload ran %d times before the first returned, want 1", n)
	}

	once.Do(func() { close(release) })
	within(t, 5*time.Second, "the late reload to be recorded", func() bool { return conf.loadedText() == t1 })
	if err := reloadInPlace(inst, conf, t0, t2, true); err == nil {
		t.Error("a reload built from the text before the late one was taken")
	}
	if got := conf.loadedText(); got != t1 {
		t.Errorf("after a refused reload the loaded text is %q, want %q", got, t1)
	}
	if err := reloadInPlace(inst, conf, t1, t2, true); err != nil {
		t.Fatalf("a reload built from the text loaded now: %v", err)
	}
	if got, blocked, n := conf.loadedText(), conf.isBlocked(), calls.Load(); got != t2 || !blocked || n != 2 {
		t.Errorf("after the last reload: text %q, blocked %v, %d reloads; want %q, true, 2", got, blocked, n, t2)
	}
}
