package fwpolicy

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	addrs := map[string]netip.Addr{
		"tv":  netip.MustParseAddr("10.42.1.5"),
		"nas": netip.MustParseAddr("10.42.1.6"),
		"new": {}, // created, not enrolled yet
	}
	rules := []Rule{
		{Proto: "tcp", Port: "8096", SourceNodeID: "tv", TargetNodeID: "nas", Description: "Jellyfin"},
		{Proto: "udp", Port: "7359", SourceNodeID: "tv"},
		{Proto: "tcp", Port: "22", TargetNodeID: "box"},
		{Proto: "tcp", Port: "443"},
		{Proto: "tcp", Port: "8096", SourceNodeID: "tv", TargetNodeID: "nas", Description: "the same, from a set"},
		{Proto: "tcp", Port: "9000", SourceNodeID: "gone"},
		{Proto: "tcp", Port: "9001", SourceNodeID: "new"},
	}
	got := Resolve(PresetPing, rules, "nas", addrs)
	want := []DeliveredRule{
		{Proto: "tcp", Port: "443"},
		{Proto: "tcp", Port: "8096", Source: "10.42.1.5"},
		{Proto: "udp", Port: "7359", Source: "10.42.1.5"},
	}
	if got.Preset != PresetPing || !reflect.DeepEqual(got.Rules, want) {
		t.Errorf("for the NAS: %+v, want %+v", got.Rules, want)
	}
	if got.Fingerprint != got.Sum() || len(got.Fingerprint) != 64 {
		t.Errorf("fingerprint %q", got.Fingerprint)
	}

	// The order of the network's rules and its sets does not matter.
	reversed := make([]Rule, len(rules))
	for i, r := range rules {
		reversed[len(rules)-1-i] = r
	}
	if again := Resolve(PresetPing, reversed, "nas", addrs); again.Fingerprint != got.Fingerprint {
		t.Errorf("another order, another fingerprint: %+v", again)
	}
	// Another device gets another policy, and so does another preset.
	box := Resolve(PresetPing, rules, "box", addrs)
	if box.Fingerprint == got.Fingerprint || len(box.Rules) != 3 {
		t.Errorf("for the box: %+v", box)
	}
	if all := Resolve(PresetAll, rules, "nas", addrs); all.Fingerprint == got.Fingerprint {
		t.Error("another preset, the same fingerprint")
	}
	// No rules: an empty list, not null, in the JSON and in the fingerprint.
	none := Resolve(PresetTCP, nil, "nas", addrs)
	if none.Rules == nil || none.Fingerprint != (Delivered{Preset: PresetTCP}).Sum() {
		t.Errorf("no rules: %+v", none)
	}
}

func TestDeliveredCheck(t *testing.T) {
	network := netip.MustParsePrefix("10.42.1.7/24")
	good := Resolve(PresetPing, []Rule{
		{Proto: "tcp", Port: "22", SourceNodeID: "a"},
		{Proto: "icmp", Port: "any"},
		{Proto: "any", Port: "5000-5010"},
	}, "me", map[string]netip.Addr{"a": netip.MustParseAddr("10.42.1.5")})
	if err := good.Check(network); err != nil {
		t.Fatalf("a good policy: %v", err)
	}
	bad := map[string]func(d *Delivered){
		"fingerprint":          func(d *Delivered) { d.Fingerprint = strings.Repeat("0", 64) },
		"a rule added after":   func(d *Delivered) { d.Rules = append(d.Rules, DeliveredRule{Proto: "tcp", Port: "any"}) },
		"unknown preset":       func(d *Delivered) { d.Preset = "open" },
		"proto":                func(d *Delivered) { d.Rules[0].Proto = "foo" },
		"port":                 func(d *Delivered) { d.Rules[0].Port = "0" },
		"icmp with a port":     func(d *Delivered) { d.Rules[1].Port = "22" },
		"source outside":       func(d *Delivered) { d.Rules[0].Source = "10.42.2.5" },
		"source a network":     func(d *Delivered) { d.Rules[0].Source = "10.42.1.0/24" },
		"source a name":        func(d *Delivered) { d.Rules[0].Source = "tv" },
		"source not canonical": func(d *Delivered) { d.Rules[0].Source = "::ffff:10.42.1.5" },
		"too many ports":       func(d *Delivered) { d.Rules[2].Port = "1-5000" },
	}
	for name, change := range bad {
		d := good
		d.Rules = append([]DeliveredRule(nil), good.Rules...)
		change(&d)
		if name != "fingerprint" {
			d.Fingerprint = d.Sum()
		}
		if name == "a rule added after" {
			d.Fingerprint = good.Fingerprint
		}
		if err := d.Check(network); err == nil {
			t.Errorf("%s: accepted %+v", name, d)
		}
	}
}
