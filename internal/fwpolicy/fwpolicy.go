// Package fwpolicy is the firewall policy of a network as the control plane
// stores it and the agent will apply it: a preset and rules that only allow
// (docs/wiki/decisions/firewall-rule-sets.md). It has no dependencies, so
// both the server and the agent can use it.
package fwpolicy

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Presets of a network (networks.firewall_preset). Every preset also
// allows what hopssh always keeps: TCP 41820 from the control plane and
// from every node, and ICMP.
const (
	// PresetAll: every protocol from every node. New networks (migration 009 on).
	PresetAll = "all"
	// PresetTCP: TCP on every port from every node. Networks created before
	// migration 009.
	PresetTCP = "tcp"
	// PresetPing: nothing beyond what hopssh always keeps; rules add the rest.
	PresetPing = "ping"
)

// ValidPreset reports whether p is a preset the server accepts.
func ValidPreset(p string) bool {
	return p == PresetAll || p == PresetTCP || p == PresetPing
}

// Limits of one policy or one rule set.
const (
	MaxRules       = 100
	MaxDescription = 200 // characters
	MaxSetName     = 64  // characters
	maxNodeID      = 64
	// MaxPorts bounds the ports that the rules reaching one device cover
	// together (Ports): a network's own rules and its attached sets. Nebula
	// keeps an entry of about 800 bytes per port of a range, per rule
	// (measured 2026-11-27: 50 MB for one rule of 1-65535, 1.7 GB for a
	// hundred), on every device, twice during a reload. 4096 is about 3 MB.
	MaxPorts = 4096
)

// Rule allows traffic into agents. An empty SourceNodeID is any node of the
// network; an empty TargetNodeID is every agent. Nodes are named by their
// ID only: a hostname is not unique, and an address can be given out again.
type Rule struct {
	Proto        string `json:"proto"`
	Port         string `json:"port"`
	SourceNodeID string `json:"sourceNodeId,omitempty"`
	TargetNodeID string `json:"targetNodeId,omitempty"`
	Description  string `json:"description,omitempty"`
}

// Validate checks one rule's syntax. Whether its nodes exist is for the
// caller, which knows the network.
func (r Rule) Validate() error {
	switch r.Proto {
	case "tcp", "udp", "any":
		if err := validPort(r.Port); err != nil {
			return err
		}
	case "icmp":
		if r.Port != "any" {
			return fmt.Errorf("port of an icmp rule must be \"any\", not %q", r.Port)
		}
	default:
		return fmt.Errorf("proto must be tcp, udp, icmp or any, not %q", r.Proto)
	}
	for _, id := range []string{r.SourceNodeID, r.TargetNodeID} {
		if len(id) > maxNodeID || !printable(id) {
			return fmt.Errorf("invalid node ID %q", id)
		}
	}
	if utf8.RuneCountInString(r.Description) > MaxDescription {
		return fmt.Errorf("description longer than %d characters", MaxDescription)
	}
	if !printable(r.Description) {
		return fmt.Errorf("description has control characters")
	}
	return nil
}

// ValidateRules checks every rule and the count. The error names the rule
// by its position, counting from 1.
func ValidateRules(rules []Rule) error {
	if len(rules) > MaxRules {
		return fmt.Errorf("more than %d rules", MaxRules)
	}
	for i, r := range rules {
		if err := r.Validate(); err != nil {
			return fmt.Errorf("rule %d: %w", i+1, err)
		}
	}
	return CheckPorts(Ports(rules))
}

// CheckPorts refuses a count of ports (Ports) above MaxPorts.
func CheckPorts(n int) error {
	if n > MaxPorts {
		return fmt.Errorf("the rules cover %d ports, more than %d; a rule for every port is port \"any\"", n, MaxPorts)
	}
	return nil
}

// Ports counts the entries Nebula keeps for valid rules: one for "any", a
// port or an icmp rule, M-N+1 for a range N-M. Rules on the same port count
// once each, since each is kept on its own.
func Ports(rules []Rule) int {
	n := 0
	for _, r := range rules {
		lo, hi, isRange := strings.Cut(r.Port, "-")
		if !isRange {
			n++
			continue
		}
		a, errA := port(lo)
		b, errB := port(hi)
		if errA != nil || errB != nil || a > b {
			n++
			continue
		}
		n += b - a + 1
	}
	return n
}

// ValidateSetName checks the name of a rule set.
func ValidateSetName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("name is required")
	}
	if utf8.RuneCountInString(name) > MaxSetName {
		return fmt.Errorf("name longer than %d characters", MaxSetName)
	}
	if !printable(name) {
		return fmt.Errorf("name has control characters")
	}
	return nil
}

// NodeIDs returns the distinct node IDs the rules name, in order of first use.
func NodeIDs(rules []Rule) []string {
	seen := map[string]bool{}
	var ids []string
	for _, r := range rules {
		for _, id := range []string{r.SourceNodeID, r.TargetNodeID} {
			if id != "" && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// validPort accepts "any", "N" or "N-M" with 1 <= N <= M <= 65535.
func validPort(p string) error {
	if p == "any" {
		return nil
	}
	lo, hi, isRange := strings.Cut(p, "-")
	if !isRange {
		hi = lo
	}
	a, errA := port(lo)
	b, errB := port(hi)
	if errA != nil || errB != nil || a > b {
		return fmt.Errorf("port must be \"any\", a number from 1 to 65535 or a range N-M, not %q", p)
	}
	return nil
}

func port(s string) (int, error) {
	// strconv.Atoi takes "+1" and "01"; a port is digits only.
	if s == "" || strings.TrimLeft(s, "0123456789") != "" || (len(s) > 1 && s[0] == '0') {
		return 0, fmt.Errorf("not a port")
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("not a port")
	}
	return n, nil
}

func printable(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, c := range s {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}
