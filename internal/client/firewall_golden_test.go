package client

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/slackhq/nebula"
	"github.com/slackhq/nebula/config"
	"github.com/slackhq/nebula/firewall"
	"gopkg.in/yaml.v3"
)

// Golden tests of every firewall block hopssh has written into a
// nebula.yaml. Nothing rewrites a device's firewall section, so a device
// enrolled in any period still runs that period's block: each must keep
// parsing, and what each allows is pinned as Nebula's own parser reads it.
// Comments, key order and yaml.Marshal's round trip don't change the
// result; a changed rule does.
//
// testdata/firewall/ holds the blocks, taken with `git show <commit>:<path>`
// (each file names its commit). The agent's template moved from
// cmd/agent/enroll.go to internal/client/enroll.go in a37656f, the client's
// from cmd/agent/client.go to internal/client/cli_join.go.

var (
	// Every block so far: all outbound, and whatever no rule allows is
	// dropped in both directions (explicitly or by Nebula's default).
	outboundAny = []string{"out any any host=any", "in default drop", "out default drop"}

	// Agents, 80fdbaa: TCP from group server only.
	agent80fdbaa = append([]string{
		"in tcp any groups=server",
		"in icmp any host=any",
	}, outboundAny...)
	// Agents, 791ac0b: groups admin, user and agent.
	agent791ac0b = append([]string{
		"in tcp 41820 groups=admin",
		"in tcp any groups=user",
		"in tcp any groups=agent",
		"in icmp any host=any",
	}, outboundAny...)
	// Agents, 41ce077 to today: one group for every node.
	agent41ce077 = append([]string{
		"in tcp 41820 groups=admin",
		"in tcp any groups=node",
		"in icmp any host=any",
	}, outboundAny...)
	// Agents in a network with the "all" preset, 2026-11-27 on: every
	// protocol from every node.
	agentAll20261127 = append([]string{
		"in tcp 41820 groups=admin",
		"in any any groups=node",
		"in icmp any host=any",
	}, outboundAny...)
	// Agents in a network with the "ping" preset, 2026-11-27 on: the agent
	// API from the control plane and from other nodes, and ICMP.
	agentPing20261127 = append([]string{
		"in tcp 41820 groups=admin",
		"in tcp 41820 groups=node",
		"in icmp any host=any",
	}, outboundAny...)
	// Clients (`hop client join`), 3f30d27 to 2026-11-27: ping only.
	client3f30d27 = append([]string{
		"in icmp any host=any",
	}, outboundAny...)
	// Clients, 2026-11-27 on: also the port the agents' keepalive dials.
	client20261127 = append([]string{
		"in tcp 41820 groups=node",
		"in icmp any host=any",
	}, outboundAny...)
)

var historicalFirewallBlocks = map[string][]string{
	"agent-80fdbaa.yaml":         agent80fdbaa,
	"agent-791ac0b.yaml":         agent791ac0b,
	"agent-41ce077.yaml":         agent41ce077,
	"agent-2026-11-27-all.yaml":  agentAll20261127,
	"agent-2026-11-27-ping.yaml": agentPing20261127,
	"client-3f30d27.yaml":        client3f30d27,
	"client-2026-11-27.yaml":     client20261127,
}

func TestFirewall_EveryHistoricalBlockParses(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "firewall", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range files {
		name := filepath.Base(f)
		seen[name] = true
		want, ok := historicalFirewallBlocks[name]
		if !ok {
			t.Errorf("%s has no entry in historicalFirewallBlocks", name)
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if got := firewallRules(t, string(data)); !slices.Equal(got, sorted(want)) {
			t.Errorf("%s allows\n  %s\nwant\n  %s", name, strings.Join(got, "\n  "), strings.Join(sorted(want), "\n  "))
		}
	}
	for name := range historicalFirewallBlocks {
		if !seen[name] {
			t.Errorf("testdata/firewall/%s is missing", name)
		}
	}
}

// TestFirewall_TodaysTemplates: what an enrollment writes today is the
// newest block of its kind. A new default changes this test and adds a
// fixture; the old blocks stay as they are.
func TestFirewall_TodaysTemplates(t *testing.T) {
	for preset, want := range map[string][]string{
		firewallPresetTCP:  agent41ce077,
		firewallPresetAll:  agentAll20261127,
		firewallPresetPing: agentPing20261127,
		"":                 agent41ce077, // a server before migration 009
		"bogus":            agent41ce077, // never wider than asked
	} {
		for _, mode := range []string{"kernel", "userspace"} {
			dir := t.TempDir()
			writeNebulaConfig(dir, "10.42.1.1", "127.0.0.1", 42001, mode, 4242, preset)
			data, err := os.ReadFile(filepath.Join(dir, "nebula.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if got := firewallRules(t, string(data)); !slices.Equal(got, sorted(want)) {
				t.Errorf("agent template, preset %q (%s TUN), allows\n  %s", preset, mode, strings.Join(got, "\n  "))
			}
		}
	}
	text := clientNebulaConfig("10.42.1.1", "127.0.0.1", 42001, "")
	if got := firewallRules(t, text); !slices.Equal(got, sorted(client20261127)) {
		t.Errorf("client template allows\n  %s", strings.Join(got, "\n  "))
	}
}

// TestFirewall_ProductionFileRoundTripped: a real agent's nebula.yaml from
// 2026-05-02, after ensureP2PConfig rewrote it with yaml.Marshal (keys
// sorted, comments gone), still has today's block.
func TestFirewall_ProductionFileRoundTripped(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "expired-cert-mbp-2026-05-02", "nebula.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := firewallRules(t, string(data)); !slices.Equal(got, sorted(agent41ce077)) {
		t.Errorf("production file allows\n  %s", strings.Join(got, "\n  "))
	}
}

// TestFirewall_WritersKeepIt: none of the agent's own rewrites of
// nebula.yaml changes what the firewall allows. "Old networks keep their
// block" rests on this.
func TestFirewall_WritersKeepIt(t *testing.T) {
	for name, block := range map[string]string{"80fdbaa": "agent-80fdbaa.yaml", "41ce077": "agent-41ce077.yaml", "all": "agent-2026-11-27-all.yaml", "ping": "agent-2026-11-27-ping.yaml"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeNebulaConfig(dir, "10.42.1.1", "127.0.0.1", 42001, "userspace", 4242, firewallPresetTCP)
			path := filepath.Join(dir, "nebula.yaml")
			replaceFirewallSection(t, path, filepath.Join("testdata", "firewall", block))
			want := firewallRules(t, readFile(t, path))

			inst := testInstance(t, "home", dir)
			inst.svcMu.Lock()
			inst.closed = true // no Nebula to reload
			inst.svcMu.Unlock()
			on, mtu, port := true, 1300, 4242
			for step, write := range []func(){
				func() { ensureP2PConfig(inst) },
				func() { _ = rewriteNebulaUnsafeRoutes(inst, []string{"10.9.0.0/16"}) },
				func() {
					_ = applyNebulaConfigUpdate(inst, &nebulaConfigUpdate{UseRelays: &on, PunchBack: &on, PunchDelay: "2s", MTU: &mtu, ListenPort: &port})
				},
				func() { upgradeTunMode(inst) },
				func() { repairNebulaYAML("home", path) },
			} {
				write()
				if got := firewallRules(t, readFile(t, path)); !slices.Equal(got, want) {
					t.Fatalf("writer %d changed the firewall to\n  %s", step, strings.Join(got, "\n  "))
				}
			}
		})
	}
}

// firewallRules is what a nebula.yaml's firewall allows, as Nebula's parser
// hands it to the firewall, one line per rule, sorted.
func firewallRules(t *testing.T, yamlText string) []string {
	t.Helper()
	l := slog.New(slog.DiscardHandler)
	c := config.NewC(l)
	if err := c.LoadString(yamlText); err != nil {
		t.Fatalf("load: %v", err)
	}
	var r ruleRecorder
	for _, inbound := range []bool{true, false} {
		if err := nebula.AddFirewallRulesFromConfig(l, inbound, c, &r); err != nil {
			t.Fatalf("Nebula rejects the firewall: %v", err)
		}
	}
	// What happens to a packet no rule allows, read as NewFirewallFromConfig
	// reads it: anything but "reject" is drop.
	for dir, key := range map[string]string{"in": "firewall.inbound_action", "out": "firewall.outbound_action"} {
		action := "drop"
		if c.GetString(key, "drop") == "reject" {
			action = "reject"
		}
		r = append(r, dir+" default "+action)
	}
	return sorted(r)
}

type ruleRecorder []string

func (r *ruleRecorder) AddRule(incoming bool, proto uint8, startPort, endPort int32, groups []string, host, cidr, localCidr, caName, caSha string) error {
	dir := "out"
	if incoming {
		dir = "in"
	}
	p := map[uint8]string{firewall.ProtoAny: "any", firewall.ProtoTCP: "tcp", firewall.ProtoUDP: "udp", firewall.ProtoICMP: "icmp"}[proto]
	if p == "" {
		p = fmt.Sprint(proto)
	}
	port := "any"
	switch {
	case startPort == firewall.PortAny && endPort == firewall.PortAny:
	case startPort == endPort:
		port = fmt.Sprint(startPort)
	default:
		port = fmt.Sprintf("%d-%d", startPort, endPort)
	}
	line := []string{dir, p, port}
	if len(groups) > 0 {
		line = append(line, "groups="+strings.Join(sorted(groups), ","))
	}
	for k, v := range map[string]string{"host": host, "cidr": cidr, "local_cidr": localCidr, "ca_name": caName, "ca_sha": caSha} {
		if v != "" {
			line = append(line, k+"="+v)
		}
	}
	slices.Sort(line[3:])
	*r = append(*r, strings.Join(line, " "))
	return nil
}

// replaceFirewallSection puts the firewall section of the block file in
// place of the one in the nebula.yaml at path.
func replaceFirewallSection(t *testing.T, path, blockFile string) {
	t.Helper()
	cfg, err := parseNebulaYAML([]byte(readFile(t, path)))
	if err != nil {
		t.Fatal(err)
	}
	block, err := parseNebulaYAML([]byte(readFile(t, blockFile)))
	if err != nil {
		t.Fatal(err)
	}
	cfg["firewall"] = block["firewall"]
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}
