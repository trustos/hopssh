package main

// Per-enrollment peer-relay state cached to disk. Populated from the
// heartbeat response on every tick; consumed by ensureP2PConfig at
// boot/restart to write `relay.am_relay` + `relay.relays` into the
// enrollment's nebula.yaml.
//
// v1 design: file-based cache + read-at-boot. The relay capability
// takes effect at the next agent restart or cert renewal (within
// 24h). Live reload-on-toggle is a planned follow-up — would require
// either Nebula's config-reload mechanism (relay.am_relay is reload-
// safe per vendor code) or a meshInstance hot-restart on change.
//
// On-disk format: small JSON, one file per enrollment.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

const relayStateFile = "relay-state.json"

// relayState is the persisted view of a node's peer-relay role +
// known peer relays. AmRelay indicates THIS node has the "relay"
// capability set on the control plane; Relays is the list of OTHER
// nodes' mesh IPs that have it (for use as `relay.relays`).
type relayState struct {
	AmRelay bool     `json:"amRelay"`
	Relays  []string `json:"relays"`
}

func relayStatePath(inst *meshInstance) string {
	return filepath.Join(inst.dir(), relayStateFile)
}

// saveRelayState writes the given state to disk, but ONLY if it
// differs from the previously persisted state. This avoids one write
// per heartbeat for the common case where the dashboard hasn't been
// touched in a while.
func saveRelayState(inst *meshInstance, amRelay bool, relays []string) error {
	if inst == nil {
		return errors.New("nil meshInstance")
	}
	// Normalize: sorted + dedup so byte equality reflects semantic equality.
	relays = normalizeRelays(relays)

	cur, _ := loadRelayState(inst)
	if cur != nil && cur.AmRelay == amRelay && stringSliceEqual(cur.Relays, relays) {
		return nil
	}

	state := relayState{AmRelay: amRelay, Relays: relays}
	data, err := json.MarshalIndent(&state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWrite(relayStatePath(inst), data, 0644)
}

// loadRelayState reads the cached state. Returns (nil, nil) if the
// file doesn't exist (no relay info yet) so callers can treat it as
// "default behavior".
func loadRelayState(inst *meshInstance) (*relayState, error) {
	data, err := os.ReadFile(relayStatePath(inst))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var s relayState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", relayStatePath(inst), err)
	}
	s.Relays = normalizeRelays(s.Relays)
	return &s, nil
}

func normalizeRelays(relays []string) []string {
	if len(relays) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(relays))
	for _, r := range relays {
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
