package client

import (
	"errors"
	"fmt"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/slackhq/nebula/config"
	"gopkg.in/yaml.v3"

	"github.com/trustos/hopssh/internal/fwpolicy"
)

// Phase 2 of docs/wiki/concepts/firewall-rules.md ("Delivery and apply"). The
// heartbeat answer of a managed network carries the policy for this device.
// The agent checks it, renders its own block from it, has Nebula build that
// block, reloads the running config in place, writes nebula.yaml, and says
// in its next heartbeat what it runs. An answer without a policy changes
// nothing: the device keeps the block it runs.

// firewallReloadLimit bounds one in-place reload, as runWithTimeout bounds
// the other Nebula calls of Phase DD.
const firewallReloadLimit = 5 * time.Second

// loadedConfig is the config a Nebula was started with and the text it was
// last loaded from, and whether that text has the firewall of a device that
// blocks incoming (firewall_block.go). Only an in-place reload changes them.
type loadedConfig struct {
	cfg     *config.C
	mu      sync.Mutex
	text    string
	blocked bool
}

func (l *loadedConfig) loadedText() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text
}

func (l *loadedConfig) isBlocked() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.blocked
}

// inPlaceConfig is a mesh service whose config can be reloaded in place.
// Both real services are; the fakes of tests are not.
type inPlaceConfig interface {
	loaded() *loadedConfig
}

// firewallState is what an instance knows about its firewall policy.
type firewallState struct {
	mu sync.Mutex
	// running is the fingerprint of the policy the instance runs, "" for the
	// block it was enrolled with or not known yet (a policy applied before a
	// restart of the agent is known again when the next one is checked).
	running string
	// svc and tried: the last policy applied or refused on svc, which is
	// not tried again on it. A restart brings another svc.
	svc   meshService
	tried string
	// ack is the report for the next heartbeat; nil until a policy came.
	ack *fwpolicy.Ack
}

// firewallAck is the report the next heartbeat carries, or nil. Blocked is
// what the running Nebula has (with none running, what its start will
// have); a device that blocks incoming reports so before any policy came,
// so a heartbeat without a report says it does not block.
func (i *meshInstance) firewallAck() *fwpolicy.Ack {
	blocked := false
	if ip, ok := i.liveSvc().(inPlaceConfig); ok && ip.loaded() != nil {
		blocked = ip.loaded().isBlocked()
	} else {
		blocked = incomingBlocked(i.dir())
	}
	i.firewall.mu.Lock()
	defer i.firewall.mu.Unlock()
	if i.firewall.ack == nil && !blocked {
		return nil
	}
	a := fwpolicy.Ack{Fingerprint: i.firewall.running}
	if i.firewall.ack != nil {
		a = *i.firewall.ack
	}
	a.Blocked = blocked
	return &a
}

// liveSvc is the instance's mesh service, nil when it has none or is closed:
// a late reload must not run on a closed instance.
func (i *meshInstance) liveSvc() meshService {
	i.svcMu.Lock()
	defer i.svcMu.Unlock()
	if i.closed {
		return nil
	}
	return i.svc
}

type firewallOutcome int

const (
	firewallApplied firewallOutcome = iota // running, and in the file
	firewallRefused                        // not applied; the reason goes in the report
	firewallLater                          // not now (a start, a changed file); the next beat tries again
)

// applyFirewall applies the policy of a heartbeat answer.
func applyFirewall(inst *meshInstance, d *fwpolicy.Delivered) {
	if d == nil {
		return
	}
	svc := inst.liveSvc()
	if svc == nil {
		return
	}
	fs := &inst.firewall
	fs.mu.Lock()
	done := fs.svc == svc && fs.tried == d.Fingerprint
	fs.mu.Unlock()
	if done {
		return
	}

	outcome, err := applyFirewallOn(inst, svc, *d)

	fs.mu.Lock()
	defer fs.mu.Unlock()
	switch outcome {
	case firewallApplied:
		if fs.running != d.Fingerprint {
			log.Printf("[firewall %s] runs policy %.12s (%d rules)", inst.name(), d.Fingerprint, len(d.Rules))
		}
		fs.running = d.Fingerprint
		fs.svc, fs.tried = svc, d.Fingerprint
		fs.ack = &fwpolicy.Ack{Fingerprint: d.Fingerprint}
	case firewallRefused:
		log.Printf("[firewall %s] refused policy %.12s: %v", inst.name(), d.Fingerprint, err)
		fs.svc, fs.tried = svc, d.Fingerprint
		fs.ack = &fwpolicy.Ack{Fingerprint: fs.running, Error: err.Error()}
	case firewallLater:
		log.Printf("[firewall %s] policy %.12s waits for the next heartbeat: %v", inst.name(), d.Fingerprint, err)
	}
}

// applyFirewallOn checks policy d, and applies it to svc and to nebula.yaml.
func applyFirewallOn(inst *meshInstance, svc meshService, d fwpolicy.Delivered) (firewallOutcome, error) {
	ip, ok := svc.(inPlaceConfig)
	ctrl := svc.NebulaControl()
	if !ok || ip.loaded() == nil || ctrl == nil {
		return firewallRefused, errors.New("this mesh can't reload its firewall in place")
	}
	conf := ip.loaded()
	path := filepath.Join(inst.dir(), "nebula.yaml")
	network, err := readMeshSubnetFromCert(path)
	if err != nil {
		return firewallLater, err
	}
	if err := d.Check(network.Masked()); err != nil {
		return firewallRefused, err
	}
	block, err := managedFirewall(d)
	if err != nil {
		return firewallRefused, err
	}

	// A start in progress replaces svc, and another apply or the switch
	// "Block incoming" is changing it; this beat leaves it alone.
	if !inst.startMu.TryLock() {
		return firewallLater, errors.New("the network is starting or changing its firewall")
	}
	defer inst.startMu.Unlock()
	unlock := lockNebulaYAML(path)
	defer unlock()

	before, wasBlocked := conf.loadedText(), conf.isBlocked()
	running, err := parseNebulaYAML([]byte(before))
	if err != nil {
		return firewallRefused, fmt.Errorf("the config Nebula runs: %w", err)
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		return firewallLater, err
	}
	onDisk, err := parseNebulaYAML(disk)
	if err != nil {
		return firewallLater, fmt.Errorf("nebula.yaml: %w", err)
	}
	// A reload applies every section, not only the firewall: the PKI one
	// reads the cert again, routes and relays written since the last start
	// would go live too. When the file changed, the next start loads it.
	if !sameApartFromFirewall(running, onDisk) {
		return firewallLater, errors.New("nebula.yaml changed since Nebula loaded it")
	}
	running["firewall"] = block
	text, err := yaml.Marshal(running)
	if err != nil {
		return firewallRefused, err
	}
	// Nebula's own builder, with this node's cert. A reload would not say
	// it refused: it logs, and the old rules stay.
	want, err := ctrl.CheckFirewall(string(text))
	if err != nil {
		return firewallRefused, fmt.Errorf("Nebula refuses it: %w", err)
	}
	// The device's own switch wins (firewall_block.go): Nebula keeps what
	// hopssh always keeps, and the file gets the policy for later.
	blocked := incomingBlocked(inst.dir())
	if blocked {
		if running["firewall"], err = blockedFirewall(); err != nil {
			return firewallRefused, err
		}
		b, err := yaml.Marshal(running)
		if err != nil {
			return firewallRefused, err
		}
		text = b
		if want, err = ctrl.CheckFirewall(string(text)); err != nil {
			return firewallRefused, fmt.Errorf("Nebula refuses the blocked firewall: %w", err)
		}
	}
	if ctrl.FirewallRuleHash() != want || conf.isBlocked() != blocked {
		if err := reloadInPlace(inst, conf, string(text), blocked); err != nil {
			return firewallLater, err
		}
		if got := ctrl.FirewallRuleHash(); got != want {
			return firewallRefused, fmt.Errorf("Nebula did not install it (rule hash %.12s, want %.12s)", got, want)
		}
	}

	if reflect.DeepEqual(onDisk["firewall"], block) {
		return firewallApplied, nil
	}
	onDisk["firewall"] = block
	out, err := yaml.Marshal(onDisk)
	if err == nil {
		err = writeNebulaYAML(path, out)
	}
	if err != nil && fileHasFirewall(path, block) {
		// Only the backup failed: the file and Nebula both have the policy.
		log.Printf("[firewall %s] nebula.yaml has the policy, its backup not: %v", inst.name(), err)
		return firewallApplied, nil
	}
	if err != nil {
		// The running rules go back to the file's, so the two agree, and a
		// restart brings no surprise.
		if rerr := reloadInPlace(inst, conf, before, wasBlocked); rerr != nil {
			log.Printf("[firewall %s] CRITICAL: nebula.yaml was not written and the old rules are not back (%v); a restart loads the file's", inst.name(), rerr)
		}
		return firewallRefused, fmt.Errorf("write nebula.yaml: %w", err)
	}
	return firewallApplied, nil
}

// reloadInPlace reloads conf from text, within firewallReloadLimit; blocked
// says whether text has the firewall of a device that blocks incoming. A
// reload that takes longer goes on alone; its text is recorded when it
// finishes.
func reloadInPlace(inst *meshInstance, conf *loadedConfig, text string, blocked bool) error {
	done := make(chan struct{})
	var err error
	runWithTimeout(inst.name(), "firewall reload", firewallReloadLimit, func() {
		err = conf.cfg.ReloadConfigString(text)
		if err == nil {
			conf.mu.Lock()
			conf.text, conf.blocked = text, blocked
			conf.mu.Unlock()
		}
		close(done)
	})
	select {
	case <-done:
		return err
	default:
		return fmt.Errorf("the reload did not finish within %v", firewallReloadLimit)
	}
}

// fileHasFirewall reports whether the nebula.yaml at path has block as its
// firewall section. writeNebulaYAML writes the file and then its backup, so
// after a failure the file may have it already.
func fileHasFirewall(path string, block interface{}) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	cfg, err := parseNebulaYAML(b)
	return err == nil && reflect.DeepEqual(cfg["firewall"], block)
}

// sameApartFromFirewall reports whether two parsed configs differ only in
// their firewall section.
func sameApartFromFirewall(a, b map[string]interface{}) bool {
	strip := func(m map[string]interface{}) map[string]interface{} {
		out := make(map[string]interface{}, len(m))
		for k, v := range m {
			if k != "firewall" {
				out[k] = v
			}
		}
		return out
	}
	return reflect.DeepEqual(strip(a), strip(b))
}

// managedFirewall renders the firewall section for a checked policy: the
// enrollment block of its preset, which keeps what the agent always keeps
// (TCP 41820 from admin and from every node, ICMP), drop for the rest, and
// one inbound entry per rule. Only the rule's protocol, port and address
// come from the server, each checked by d.Check.
func managedFirewall(d fwpolicy.Delivered) (interface{}, error) {
	base, err := parseNebulaYAML([]byte(agentFirewallYAML(d.Preset)))
	if err != nil {
		return nil, err
	}
	fw, ok := base["firewall"].(map[string]interface{})
	if !ok {
		return nil, errors.New("the preset's block has no firewall")
	}
	inbound, _ := fw["inbound"].([]interface{})
	fw["inbound_action"] = "drop"
	for _, r := range d.Rules {
		e := map[string]interface{}{"port": r.Port, "proto": r.Proto}
		if r.Source == "" {
			e["groups"] = []interface{}{"node"}
		} else {
			a, err := netip.ParseAddr(r.Source)
			if err != nil {
				return nil, err
			}
			e["cidr"] = netip.PrefixFrom(a, a.BitLen()).String()
		}
		inbound = append(inbound, e)
	}
	fw["inbound"] = inbound
	// As the file reads back, so it compares equal with what a parse of the
	// file gives.
	b, err := yaml.Marshal(fw)
	if err != nil {
		return nil, err
	}
	var out interface{}
	if err := yaml.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}
