package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/trustos/hopssh/internal/blocklist"
)

// docs/wiki/concepts/deleted-devices.md: the heartbeat answer carries the
// certificates of the network's deleted devices. The device keeps the list
// in its enrollment's directory and gives Nebula the entries that still
// need blocking as pki.blocklist: by an in-place reload when the list
// changes, and at every start. nebula.yaml never has it; the agent owns
// pki.blocklist. A device deleted in the dashboard is then refused by every
// device within a heartbeat, instead of when its certificate expires.

// blocklistFile is the last list the server sent, in the enrollment's
// directory.
const blocklistFile = "blocklist.json"

// blocklistState is what an instance last saved.
type blocklistState struct {
	mu    sync.Mutex
	saved []byte // the file's content, nil before the first save or load
}

// loadBlocklist is the list saved in dir. Missing, unreadable or invalid:
// no list. A bad file must never keep a network from starting; the next
// heartbeat brings the list again.
func loadBlocklist(dir string) []blocklist.Entry {
	raw, err := os.ReadFile(filepath.Join(dir, blocklistFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		log.Printf("[blocklist] %s: %v; starting without it", dir, err)
		return nil
	}
	var list []blocklist.Entry
	if err := json.Unmarshal(raw, &list); err != nil {
		log.Printf("[blocklist] %s: %v; starting without it", dir, err)
		return nil
	}
	if err := blocklist.Check(list); err != nil {
		log.Printf("[blocklist] %s: %v; starting without it", dir, err)
		return nil
	}
	return list
}

// activeBlocklist is what Nebula is given now from list: the entries that
// still need blocking, never this device's own certificate.
func activeBlocklist(dir string, list []blocklist.Entry) []string {
	own := ""
	if pem, err := os.ReadFile(filepath.Join(dir, "node.crt")); err == nil {
		own, _, _ = blocklist.Fingerprint(pem)
	}
	return blocklist.Active(list, time.Now(), own)
}

// setBlocklist puts fps into cfg as pki.blocklist, or takes it out when
// there are none.
func setBlocklist(cfg map[string]interface{}, fps []string) error {
	pki, ok := cfg["pki"].(map[string]interface{})
	if len(fps) == 0 {
		if ok {
			delete(pki, "blocklist")
		}
		return nil
	}
	if !ok {
		return errors.New("the config has no pki section")
	}
	l := make([]interface{}, len(fps))
	for i, fp := range fps {
		l[i] = fp
	}
	pki["blocklist"] = l
	return nil
}

// applyBlocklist takes the blocklist of a heartbeat answer: saved, and
// given to the running Nebula. Absent (an older server, or one that could
// not read its database): the device keeps what it has. Decoded on its own,
// so a list the agent refuses changes nothing else of the answer.
func applyBlocklist(inst *meshInstance, raw json.RawMessage) {
	takeBlocklist(inst, raw, false)
}

// applyBlocklistAtStart is applyBlocklist for the heartbeat a start sends
// (warmPeersFromHeartbeat), which runs with startMu held by that start.
func applyBlocklistAtStart(inst *meshInstance, raw json.RawMessage) {
	takeBlocklist(inst, raw, true)
}

func takeBlocklist(inst *meshInstance, raw json.RawMessage, held bool) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return
	}
	var list []blocklist.Entry
	if err := json.Unmarshal(raw, &list); err != nil {
		log.Printf("[blocklist %s] refused the server's list: %v", inst.name(), err)
		return
	}
	if err := blocklist.Check(list); err != nil {
		log.Printf("[blocklist %s] refused the server's list: %v", inst.name(), err)
		return
	}
	if list == nil {
		list = []blocklist.Entry{}
	}
	if err := saveBlocklist(inst, list); err != nil {
		// Applied anyway; the next start goes without it until a heartbeat.
		log.Printf("[blocklist %s] not saved: %v", inst.name(), err)
	}
	fps := activeBlocklist(inst.dir(), list)
	if !held {
		if svc := inst.liveSvc(); svc == nil || blocklistSettled(svc, fps) {
			return // nothing to change, or the next start loads the saved list
		}
		// A start, a renewal, a firewall apply or the switch "Block incoming"
		// is changing the Nebula; this beat leaves it alone.
		if !inst.startMu.TryLock() {
			log.Printf("[blocklist %s] waits for the next heartbeat: the network is starting or changing its config", inst.name())
			return
		}
		defer inst.startMu.Unlock()
	}
	// Taken under startMu: a restart may have replaced the one of before.
	if svc := inst.liveSvc(); svc != nil {
		if err := applyBlocklistOn(inst, svc, fps); err != nil {
			log.Printf("[blocklist %s] waits for the next heartbeat: %v", inst.name(), err)
		}
	}
}

// applySavedBlocklist gives the Nebula of svc, just started, the list saved
// now; the caller holds startMu. A heartbeat during a restart saves its list
// and leaves the Nebula alone, so a list saved after the start read the file
// would otherwise wait for the next beat.
func applySavedBlocklist(inst *meshInstance, svc meshService) {
	fps := activeBlocklist(inst.dir(), loadBlocklist(inst.dir()))
	if err := applyBlocklistOn(inst, svc, fps); err != nil {
		log.Printf("[blocklist %s] waits for the next heartbeat: %v", inst.name(), err)
	}
}

// saveBlocklist writes list to the enrollment's directory when it changed.
func saveBlocklist(inst *meshInstance, list []blocklist.Entry) error {
	data, err := json.Marshal(list)
	if err != nil {
		return err
	}
	bs := &inst.blocklist
	bs.mu.Lock()
	defer bs.mu.Unlock()
	path := filepath.Join(inst.dir(), blocklistFile)
	if bs.saved == nil {
		bs.saved, _ = os.ReadFile(path)
	}
	if bytes.Equal(bs.saved, data) {
		return nil
	}
	if err := atomicWrite(path, data, 0o644); err != nil {
		return err
	}
	bs.saved = data
	return nil
}

// blocklistSettled: nothing to do for fps on svc. Its Nebula refuses exactly
// fps, or there is none to reload in place (its next start loads the list).
// Nebula's CA pool is what counts, not the text it loaded: a reload that
// can't read the CA keeps the old pool and only logs.
func blocklistSettled(svc meshService, fps []string) bool {
	ip, ok := svc.(inPlaceConfig)
	ctrl := svc.NebulaControl()
	if !ok || ip.loaded() == nil || ctrl == nil {
		return true
	}
	return slices.Equal(ctrl.CABlocklist(), fps)
}

// applyBlocklistOn gives the running Nebula of svc the blocklist fps by an
// in-place reload, when it refuses another one. The caller holds startMu.
func applyBlocklistOn(inst *meshInstance, svc meshService, fps []string) error {
	if blocklistSettled(svc, fps) {
		return nil
	}
	conf := svc.(inPlaceConfig).loaded()
	path := filepath.Join(inst.dir(), "nebula.yaml")
	unlock := lockNebulaYAML(path)
	defer unlock()

	base := conf.loadedText()
	running, err := parseNebulaYAML([]byte(base))
	if err != nil {
		return fmt.Errorf("the config Nebula runs: %w", err)
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	onDisk, err := parseNebulaYAML(disk)
	if err != nil {
		return fmt.Errorf("nebula.yaml: %w", err)
	}
	// A reload applies every section; when the file changed, the next start
	// loads it with the list.
	if !sameApartFromOverlays(running, onDisk) {
		return errors.New("nebula.yaml changed since Nebula loaded it")
	}
	if err := setBlocklist(running, fps); err != nil {
		return err
	}
	text, err := yaml.Marshal(running)
	if err != nil {
		return err
	}
	// The same text again when an earlier reload could not read the CA:
	// Nebula loads the CA pool at every reload.
	if err := reloadInPlace(inst, conf, base, string(text), conf.isBlocked()); err != nil {
		return err
	}
	if got := svc.NebulaControl().CABlocklist(); !slices.Equal(got, fps) {
		return fmt.Errorf("Nebula refuses %d certificates, not the %d of the list (see its log: the CA may not have loaded)", len(got), len(fps))
	}
	log.Printf("[blocklist %s] certificates of deleted devices refused: %d", inst.name(), len(fps))
	return nil
}
