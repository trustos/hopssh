package fwpolicy

import (
	"fmt"
	"strings"
	"testing"
)

func TestRuleValidate(t *testing.T) {
	ok := []Rule{
		{Proto: "tcp", Port: "any"},
		{Proto: "tcp", Port: "22"},
		{Proto: "udp", Port: "1"},
		{Proto: "udp", Port: "65535"},
		{Proto: "any", Port: "5000-5010"},
		{Proto: "tcp", Port: "80-80"},
		{Proto: "icmp", Port: "any"},
		{Proto: "tcp", Port: "443", SourceNodeID: "0f8a3c1e-2b9d-4c7e-a5f1-6d2e8b4a9c3f", TargetNodeID: "1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d"},
		{Proto: "tcp", Port: "443", Description: "Jellyfin, from the TV — ok"},
		{Proto: "tcp", Port: "443", Description: strings.Repeat("я", MaxDescription)},
	}
	for _, r := range ok {
		if err := r.Validate(); err != nil {
			t.Errorf("%+v: %v", r, err)
		}
	}
	bad := []Rule{
		{Proto: "", Port: "any"},
		{Proto: "TCP", Port: "any"},
		{Proto: "gre", Port: "any"},
		{Proto: "tcp", Port: ""},
		{Proto: "tcp", Port: "0"},
		{Proto: "tcp", Port: "65536"},
		{Proto: "tcp", Port: "-1"},
		{Proto: "tcp", Port: "+22"},
		{Proto: "tcp", Port: "022"},
		{Proto: "tcp", Port: " 22"},
		{Proto: "tcp", Port: "22 "},
		{Proto: "tcp", Port: "0x16"},
		{Proto: "tcp", Port: "10-5"},
		{Proto: "tcp", Port: "5-"},
		{Proto: "tcp", Port: "-5"},
		{Proto: "tcp", Port: "1-2-3"},
		{Proto: "tcp", Port: "any-5"},
		{Proto: "tcp", Port: "fragment"},
		{Proto: "icmp", Port: "8"},
		{Proto: "icmp", Port: ""},
		{Proto: "tcp", Port: "22", SourceNodeID: strings.Repeat("a", 65)},
		{Proto: "tcp", Port: "22", TargetNodeID: "a\nb"},
		{Proto: "tcp", Port: "22", SourceNodeID: "\xff"},
		{Proto: "tcp", Port: "22", Description: strings.Repeat("a", MaxDescription+1)},
		{Proto: "tcp", Port: "22", Description: "a\x00b"},
		{Proto: "tcp", Port: "22", Description: "line\nbreak"},
	}
	for _, r := range bad {
		if err := r.Validate(); err == nil {
			t.Errorf("%+v: accepted", r)
		}
	}
	// A bidi override (U+202E) is a format character, not a control one, and
	// is accepted: a description is shown as text and never applied, and
	// refusing format characters would refuse emoji joined with U+200D.
	if err := (Rule{Proto: "tcp", Port: "22", Description: "‮txt.exe"}).Validate(); err != nil {
		t.Errorf("bidi override: %v", err)
	}
}

func TestValidateRules(t *testing.T) {
	if err := ValidateRules(nil); err != nil {
		t.Errorf("no rules: %v", err)
	}
	max := make([]Rule, MaxRules)
	for i := range max {
		max[i] = Rule{Proto: "tcp", Port: fmt.Sprint(i + 1)}
	}
	if err := ValidateRules(max); err != nil {
		t.Errorf("%d rules: %v", MaxRules, err)
	}
	if err := ValidateRules(append(max, Rule{Proto: "tcp", Port: "any"})); err == nil {
		t.Errorf("%d rules accepted", MaxRules+1)
	}
	err := ValidateRules([]Rule{{Proto: "tcp", Port: "22"}, {Proto: "tcp", Port: "0"}})
	if err == nil || !strings.HasPrefix(err.Error(), "rule 2: ") {
		t.Errorf("error %v, want one naming rule 2", err)
	}
}

// Nebula keeps one entry per port of a range on every device the rules
// reach: one rule for 1-65535 took 50 MB, a hundred of them 1.7 GB
// (measured 2026-11-27). "any" is one entry.
func TestValidateRules_Ports(t *testing.T) {
	for _, rules := range [][]Rule{
		{{Proto: "tcp", Port: fmt.Sprintf("1-%d", MaxPorts)}},
		{{Proto: "tcp", Port: "1-2048"}, {Proto: "udp", Port: "1-2048"}},
		{{Proto: "tcp", Port: "any"}, {Proto: "udp", Port: "any"}, {Proto: "any", Port: "any"}, {Proto: "icmp", Port: "any"}},
	} {
		if err := ValidateRules(rules); err != nil {
			t.Errorf("%+v: %v", rules, err)
		}
	}
	for _, rules := range [][]Rule{
		{{Proto: "tcp", Port: "1-65535"}},
		{{Proto: "tcp", Port: fmt.Sprintf("1-%d", MaxPorts+1)}},
		{{Proto: "tcp", Port: "1-2048"}, {Proto: "udp", Port: "1-2048"}, {Proto: "tcp", Port: "22"}},
	} {
		err := ValidateRules(rules)
		if err == nil {
			t.Errorf("%+v: accepted", rules)
		} else if !strings.Contains(err.Error(), `"any"`) {
			t.Errorf("%+v: %q does not point to \"any\"", rules, err)
		}
	}
}

func TestPorts(t *testing.T) {
	for _, tc := range []struct {
		rules []Rule
		want  int
	}{
		{nil, 0},
		{[]Rule{{Proto: "tcp", Port: "any"}}, 1},
		{[]Rule{{Proto: "icmp", Port: "any"}}, 1},
		{[]Rule{{Proto: "tcp", Port: "22"}}, 1},
		{[]Rule{{Proto: "tcp", Port: "80-80"}}, 1},
		{[]Rule{{Proto: "udp", Port: "5000-5010"}}, 11},
		{[]Rule{{Proto: "tcp", Port: "1-65535"}, {Proto: "tcp", Port: "22"}}, 65536},
	} {
		if got := Ports(tc.rules); got != tc.want {
			t.Errorf("Ports(%+v) = %d, want %d", tc.rules, got, tc.want)
		}
	}
}

func TestValidateSetName(t *testing.T) {
	for _, name := range []string{"web", "Home servers", strings.Repeat("é", MaxSetName)} {
		if err := ValidateSetName(name); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
	for _, name := range []string{"", "   ", strings.Repeat("a", MaxSetName+1), "a\tb"} {
		if err := ValidateSetName(name); err == nil {
			t.Errorf("%q: accepted", name)
		}
	}
}

func TestValidPreset(t *testing.T) {
	for _, p := range []string{PresetAll, PresetTCP, PresetPing} {
		if !ValidPreset(p) {
			t.Errorf("%q refused", p)
		}
	}
	for _, p := range []string{"", "ALL", "none", "udp"} {
		if ValidPreset(p) {
			t.Errorf("%q accepted", p)
		}
	}
}

func TestNodeIDs(t *testing.T) {
	got := NodeIDs([]Rule{
		{SourceNodeID: "a", TargetNodeID: "b"},
		{SourceNodeID: "b"},
		{TargetNodeID: "c"},
		{},
		{SourceNodeID: "a", TargetNodeID: "a"},
	})
	if fmt.Sprint(got) != "[a b c]" {
		t.Errorf("got %v, want [a b c]", got)
	}
	if NodeIDs(nil) != nil {
		t.Error("no rules: want nil")
	}
}
