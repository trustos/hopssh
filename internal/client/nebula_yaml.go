package client

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// Every writer of a nebula.yaml reads it, changes a part and writes it back:
// renewal (applyNebulaConfigUpdate), routes (heartbeat goroutine), the boot
// healers (ensureP2PConfig, upgradeTunMode, healListenPortYAML) and the
// enrollment templates. They run on different goroutines, so each holds the
// file's lock from its read to its write; otherwise the later write carries
// the other's old values (TestNebulaYAML_WritersKeepEachOthersChanges).
// TestNebulaYAML_EveryWriterLocks finds a writer that doesn't.

var nebulaYAMLLocks sync.Map // cleaned path -> *sync.Mutex

// lockNebulaYAML locks the nebula.yaml at path and returns the unlock.
func lockNebulaYAML(path string) (unlock func()) {
	m, _ := nebulaYAMLLocks.LoadOrStore(filepath.Clean(path), new(sync.Mutex))
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// writeNebulaYAML replaces the nebula.yaml at path and then its backup,
// <path>.bak, which repairNebulaYAML restores from. The caller holds
// lockNebulaYAML(path).
func writeNebulaYAML(path string, data []byte) error {
	if err := atomicWrite(path, data, 0644); err != nil {
		return err
	}
	return atomicWrite(path+".bak", data, 0644)
}

// nebulaYAMLKeys are the top-level keys every config hopssh writes has. A
// file without one of them is cut short: yaml.Marshal writes keys in
// alphabetical order, so a cut loses pki, static_host_map or tun first, and
// a file without firewall drops every inbound packet.
var nebulaYAMLKeys = []string{"pki", "static_host_map", "lighthouse", "listen", "tun", "firewall"}

// checkNebulaYAML says what is wrong with a nebula.yaml's content, or nil.
func checkNebulaYAML(data []byte) error {
	cfg, err := parseNebulaYAML(data)
	if err != nil {
		return err
	}
	for _, k := range nebulaYAMLKeys {
		if _, ok := cfg[k]; !ok {
			return fmt.Errorf("no %q section", k)
		}
	}
	return nil
}

// repairNebulaYAML runs before a network starts. A nebula.yaml that is
// missing, empty, unreadable or cut short is replaced by its backup when the
// backup is whole; nothing else would ever rewrite it, and the network would
// stay down for good. A whole file with no backup yet (written by a version
// before backups) gets one.
func repairNebulaYAML(name, path string) {
	unlock := lockNebulaYAML(path)
	defer unlock()

	data, readErr := os.ReadFile(path)
	bad := readErr
	if bad == nil {
		bad = checkNebulaYAML(data)
	}
	if bad == nil {
		if _, err := os.Stat(path + ".bak"); os.IsNotExist(err) {
			if err := atomicWrite(path+".bak", data, 0644); err != nil {
				log.Printf("[agent %s] WARNING: back up nebula.yaml: %v", name, err)
			}
		}
		return
	}

	backup, err := os.ReadFile(path + ".bak")
	if err == nil {
		err = checkNebulaYAML(backup)
	}
	if err != nil {
		log.Printf("[agent %s] nebula.yaml is damaged (%v) and has no usable backup (%v)", name, bad, err)
		return
	}
	if err := atomicWrite(path, backup, 0644); err != nil {
		log.Printf("[agent %s] WARNING: restore nebula.yaml from backup: %v", name, err)
		return
	}
	log.Printf("[agent %s] nebula.yaml was damaged (%v); restored from nebula.yaml.bak", name, bad)
}
