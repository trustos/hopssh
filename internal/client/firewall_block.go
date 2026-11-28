package client

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/trustos/hopssh/internal/fwpolicy"
)

// Phase 3 of docs/wiki/concepts/firewall-rules.md: the device's own switch
// "Block incoming from other devices", per network. While it is on, Nebula
// runs what hopssh always keeps (TCP 41820 from admin and from every node,
// ICMP) and drops the rest, whatever the network's rules say. nebula.yaml
// keeps the network's rules, and a policy from the heartbeat still goes
// there, so switching it off brings back what the network allows. Nothing
// the server sends turns it off.

// blockIncomingFile is in an enrollment's directory while the switch is on.
// Every start of Nebula reads it (loadNebulaConfig); leaving the network
// removes it with the directory.
const blockIncomingFile = "block-incoming"

// incomingBlocked reports whether the switch is on for the enrollment in
// dir. A file that can't be looked at counts as on: the switch fails closed,
// and hopssh's own access stays either way.
func incomingBlocked(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, blockIncomingFile))
	return !errors.Is(err, fs.ErrNotExist)
}

// blockedFirewall is the firewall section of a device that blocks incoming:
// the "ping" preset without rules, which is what hopssh always keeps.
func blockedFirewall() (interface{}, error) {
	return managedFirewall(fwpolicy.Delivered{Preset: fwpolicy.PresetPing})
}

// withOverlays is the config text with, when blocked, its firewall section
// replaced by blockedFirewall, and pki.blocklist set to fps (blocklist.go).
func withOverlays(raw []byte, blocked bool, fps []string) (string, error) {
	cfg, err := parseNebulaYAML(raw)
	if err != nil {
		return "", err
	}
	if blocked {
		if cfg["firewall"], err = blockedFirewall(); err != nil {
			return "", err
		}
	}
	if err := setBlocklist(cfg, fps); err != nil {
		return "", err
	}
	out, err := yaml.Marshal(cfg)
	return string(out), err
}

// errNotLive: the switch is saved, and the network's next start applies it.
var errNotLive = errors.New("the network is not running; its next start applies it")

// errSwitchNotSaved: nothing changed.
var errSwitchNotSaved = errors.New("the switch could not be saved")

// setIncomingBlocked turns the switch on or off for inst: saved first, so
// that every later start has it, then applied to the running Nebula by an
// in-place reload of its firewall alone. Only the firewall section of the
// text Nebula loaded changes, so nebula.yaml is not written and may differ
// from it. errNotLive when no Nebula runs.
func setIncomingBlocked(inst *meshInstance, on bool) error {
	inst.startMu.Lock()
	defer inst.startMu.Unlock()
	if err := saveIncomingBlocked(inst.dir(), on); err != nil {
		return fmt.Errorf("%w: %v", errSwitchNotSaved, err)
	}
	return applyIncomingBlocked(inst, on)
}

// reapplyIncomingBlocked is the heartbeat's retry of the switch, when the
// running Nebula has the other state: a switch refused because an earlier
// reload had not finished, or one that such a reload undid when it
// finished. Nothing else gives it to Nebula before its next start.
func reapplyIncomingBlocked(inst *meshInstance) {
	ip, ok := inst.liveSvc().(inPlaceConfig)
	if !ok || ip.loaded() == nil {
		return
	}
	on := incomingBlocked(inst.dir())
	if ip.loaded().isBlocked() == on {
		return
	}
	// A start, a renewal, a firewall apply or the switch itself is under
	// way; the next beat looks again.
	if !inst.startMu.TryLock() {
		return
	}
	defer inst.startMu.Unlock()
	if err := applyIncomingBlocked(inst, on); err != nil {
		log.Printf("[firewall %s] block incoming %v waits for the next heartbeat: %v", inst.name(), on, err)
		return
	}
	log.Printf("[firewall %s] block incoming %v, as the switch says", inst.name(), on)
}

// applyIncomingBlocked gives the running Nebula of inst the switch; the
// caller holds startMu.
func applyIncomingBlocked(inst *meshInstance, on bool) error {
	svc := inst.liveSvc()
	if svc == nil {
		return errNotLive
	}
	ip, ok := svc.(inPlaceConfig)
	ctrl := svc.NebulaControl()
	if !ok || ip.loaded() == nil || ctrl == nil {
		return errors.New("this network can't change its firewall while it runs; it applies at the next start")
	}
	conf := ip.loaded()
	path := filepath.Join(inst.dir(), "nebula.yaml")
	unlock := lockNebulaYAML(path)
	defer unlock()

	base := conf.loadedText()
	running, err := parseNebulaYAML([]byte(base))
	if err != nil {
		return fmt.Errorf("the config Nebula runs: %w", err)
	}
	if on {
		running["firewall"], err = blockedFirewall()
		if err != nil {
			return err
		}
	} else {
		// What a start would load: the network's rules, as nebula.yaml has them.
		disk, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		onDisk, err := parseNebulaYAML(disk)
		if err != nil {
			return fmt.Errorf("nebula.yaml: %w", err)
		}
		if onDisk["firewall"] == nil {
			return errors.New("nebula.yaml has no firewall section")
		}
		running["firewall"] = onDisk["firewall"]
	}
	text, err := yaml.Marshal(running)
	if err != nil {
		return err
	}
	want, err := ctrl.CheckFirewall(string(text))
	if err != nil {
		return fmt.Errorf("Nebula refuses it: %w", err)
	}
	if ctrl.FirewallRuleHash() == want && conf.isBlocked() == on {
		return nil
	}
	if err := reloadInPlace(inst, conf, base, string(text), on); err != nil {
		return err
	}
	if got := ctrl.FirewallRuleHash(); got != want {
		return fmt.Errorf("Nebula did not install it (rule hash %.12s, want %.12s)", got, want)
	}
	return nil
}

// saveIncomingBlocked writes or removes the switch's file in dir.
func saveIncomingBlocked(dir string, on bool) error {
	path := filepath.Join(dir, blockIncomingFile)
	if on {
		return atomicWrite(path, []byte("on\n"), 0o644)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
