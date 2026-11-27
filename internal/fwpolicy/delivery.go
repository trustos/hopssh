package fwpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
)

// Phase 2 (docs/wiki/concepts/firewall-rules.md, "Delivery and apply"): the
// heartbeat answer of a managed network carries the policy for the device
// that asked, and the device's next heartbeat says what it runs.

// Delivered is a network's policy as one device gets it: only the rules
// whose target is every agent or this device, with each source node's mesh
// address. Rules are sorted and without repeats, so the same policy has the
// same fingerprint whatever order the network and its sets list it in.
type Delivered struct {
	Fingerprint string          `json:"fingerprint"`
	Preset      string          `json:"preset"`
	Rules       []DeliveredRule `json:"rules"`
}

// DeliveredRule allows one protocol and port from one node (Source, a mesh
// address) or from every node of the network (Source empty).
type DeliveredRule struct {
	Proto  string `json:"proto"`
	Port   string `json:"port"`
	Source string `json:"source,omitempty"`
}

// Ack is what a device reports in its next heartbeat: the fingerprint of
// the policy it runs (empty for the block it was enrolled with) and, when it
// refused the last one it got, why.
type Ack struct {
	Fingerprint string `json:"fingerprint"`
	Error       string `json:"error,omitempty"`
}

// Resolve builds the policy for device nodeID. addrs maps each node of the
// network to its mesh address; a rule whose source node has none (deleted,
// or not enrolled yet) is left out, as it matches nothing.
func Resolve(preset string, rules []Rule, nodeID string, addrs map[string]netip.Addr) Delivered {
	out := []DeliveredRule{}
	for _, r := range rules {
		if r.TargetNodeID != "" && r.TargetNodeID != nodeID {
			continue
		}
		d := DeliveredRule{Proto: r.Proto, Port: r.Port}
		if r.SourceNodeID != "" {
			a, ok := addrs[r.SourceNodeID]
			if !ok || !a.IsValid() {
				continue
			}
			d.Source = a.String()
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Proto != b.Proto {
			return a.Proto < b.Proto
		}
		return a.Port < b.Port
	})
	uniq := out[:0]
	for i, r := range out {
		if i == 0 || r != out[i-1] {
			uniq = append(uniq, r)
		}
	}
	d := Delivered{Preset: preset, Rules: uniq}
	d.Fingerprint = d.Sum()
	return d
}

// Sum is the fingerprint of the preset and the rules: SHA-256 of their JSON,
// in hex.
func (d Delivered) Sum() string {
	rules := d.Rules
	if rules == nil {
		rules = []DeliveredRule{}
	}
	b, err := json.Marshal(struct {
		Preset string          `json:"preset"`
		Rules  []DeliveredRule `json:"rules"`
	}{d.Preset, rules})
	if err != nil {
		panic(err) // strings only
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Check is what the device verifies before it applies a policy: its
// fingerprint, a known preset, each rule's protocol and port, a source that
// is an address of the device's own network, and the ports bound.
func (d Delivered) Check(network netip.Prefix) error {
	if d.Fingerprint != d.Sum() {
		return fmt.Errorf("fingerprint does not match the policy")
	}
	if !ValidPreset(d.Preset) {
		return fmt.Errorf("unknown preset %q", d.Preset)
	}
	rules := make([]Rule, 0, len(d.Rules))
	for i, r := range d.Rules {
		rule := Rule{Proto: r.Proto, Port: r.Port}
		if err := rule.Validate(); err != nil {
			return fmt.Errorf("rule %d: %w", i+1, err)
		}
		if r.Source != "" {
			a, err := netip.ParseAddr(r.Source)
			if err != nil || a.String() != r.Source || !network.Contains(a) {
				return fmt.Errorf("rule %d: source %q is not an address of %s", i+1, r.Source, network)
			}
		}
		rules = append(rules, rule)
	}
	return CheckPorts(Ports(rules))
}
