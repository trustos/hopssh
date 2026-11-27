package client

import (
	"net/netip"
	"reflect"
	"slices"
	"testing"

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
