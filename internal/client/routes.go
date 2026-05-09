package client

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Roadmap #5 — Subnet routing.
//
// The server pushes the requesting node's `routes` list in the heartbeat
// response (see internal/api/renew.go::Heartbeat). The agent applies the
// list by:
//
//   1. Persisting the list to <enrollment>/routes.json so we can detect
//      changes across restarts (and so the Nebula config rewrite is
//      idempotent across the no-change-since-last-tick case).
//   2. Rewriting the `tun.unsafe_routes` block in <enrollment>/nebula.yaml
//      ONLY when the list actually changed.
//   3. Triggering a Nebula reload via reloadNebula (same path used by
//      cert renewal) so the new routes take effect without restarting
//      the agent.
//
// On Linux + macOS, Nebula installs the unsafe_routes into the kernel
// routing table on the next reload. The node must run as root for the
// kernel to accept routing-table writes — already true in system-mode
// install. Document this in the user-facing setup guide.

const routesStateFile = "routes.json"

// applyRoutesUpdate reconciles the server-supplied route list with
// what's persisted to disk. No-op when the list hasn't changed.
//
// Pure best-effort: any error is logged + swallowed. Misapplied routes
// don't break the mesh — they just mean the node isn't a gateway for
// the configured subnets until the next successful heartbeat.
func applyRoutesUpdate(inst *meshInstance, fresh []string) {
	if inst == nil {
		return
	}
	cleaned := normaliseRoutes(fresh)
	prior := loadPersistedRoutes(inst)
	if routesEqual(cleaned, prior) {
		return
	}
	if err := persistRoutes(inst, cleaned); err != nil {
		log.Printf("[routes %s] WARNING: persist failed: %v", inst.name(), err)
		return
	}
	if err := rewriteNebulaUnsafeRoutes(inst, cleaned); err != nil {
		log.Printf("[routes %s] WARNING: nebula.yaml rewrite failed: %v", inst.name(), err)
		return
	}
	log.Printf("[routes %s] applied %d route(s); reloading Nebula", inst.name(), len(cleaned))
	// reloadNebula closes the running svc + spawns a new one with
	// the same on-disk config, picking up the new unsafe_routes block.
	// Reuses the same retry+backoff infrastructure cert renewal uses.
	// Logs internally; no error to propagate to the heartbeat caller.
	reloadNebula(inst)
}

// normaliseRoutes deduplicates, sorts, and trims the server-supplied
// list. Sorted form is the canonical form persisted to disk so route-
// equality can be a string compare.
func normaliseRoutes(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, r := range in {
		c := strings.TrimSpace(r)
		if c == "" {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// routesEqual treats two normalised slices as equal iff they have the
// same length + same string content position-by-position.
func routesEqual(a, b []string) bool {
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

func loadPersistedRoutes(inst *meshInstance) []string {
	path := filepath.Join(inst.dir(), routesStateFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func persistRoutes(inst *meshInstance, routes []string) error {
	path := filepath.Join(inst.dir(), routesStateFile)
	if len(routes) == 0 {
		// Empty list — remove the state file so next startup reads
		// (nil, nil) cleanly.
		_ = os.Remove(path)
		return nil
	}
	data := []byte(strings.Join(routes, "\n") + "\n")
	return atomicWrite(path, data, 0644)
}

// rewriteNebulaUnsafeRoutes loads the existing nebula.yaml, swaps in
// the new `tun.unsafe_routes` list (or removes it when empty), and
// writes the result back. Preserves all other config keys verbatim.
//
// Nebula's unsafe_routes shape:
//
//   tun:
//     unsafe_routes:
//       - route: 10.0.0.0/16
//       - route: 192.168.50.0/24
//
// (per-entry can also carry mtu / metric / install — we don't expose
// those today but the array-of-objects shape leaves room for it.)
func rewriteNebulaUnsafeRoutes(inst *meshInstance, routes []string) error {
	cfgPath := filepath.Join(inst.dir(), "nebula.yaml")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	tunBlock, _ := cfg["tun"].(map[string]any)
	if tunBlock == nil {
		tunBlock = map[string]any{}
	}
	if len(routes) == 0 {
		delete(tunBlock, "unsafe_routes")
	} else {
		entries := make([]map[string]any, 0, len(routes))
		for _, r := range routes {
			entries = append(entries, map[string]any{"route": r})
		}
		tunBlock["unsafe_routes"] = entries
	}
	cfg["tun"] = tunBlock
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, out, 0644)
}
