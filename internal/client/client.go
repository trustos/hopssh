package client

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/trustos/hopssh/internal/nebulacfg"
)

// connect is the lifted body of v0.10.34's runServe-local connectFn closure.
// It brings up a single named enrollment with retry+backoff, wiring
// inst.restartFn for v0.10.36 watchdog auto-recovery.
//
// Idempotent: if the instance is already up, return nil.
func (c *Client) connect(name string) error {
	if c.connectOverride != nil {
		return c.connectOverride(name)
	}
	e := c.enrolls.Get(name)
	if e == nil {
		return fmt.Errorf("enrollment %q not found", name)
	}
	if existing := c.instances.get(name); existing != nil && existing.control() != nil {
		return nil // already up
	}
	// Drop any stale instance entry (e.g. previous start failed and left
	// a half-initialized inst in the registry) so the new startInstance
	// gets a clean slate.
	if old := c.instances.remove(name); old != nil {
		old.close()
		c.servers.shutdownInstance(name)
	}

	listenPort := e.ListenPort
	if listenPort == 0 {
		listenPort = nebulacfg.ListenPort
	}
	devName := meshIfaceName(name)

	try := func() (*meshInstance, error) {
		waitForUDPPortFreeFn(listenPort, 3*time.Second)
		waitForTUNDeviceFreeFn(devName, 3*time.Second)

		inst := newMeshInstance(e)
		// v0.10.36: re-wire restartFn on every fresh instance so the
		// watchdog can recover this one too. Recursion is fine — each
		// restart constructs a new closure bound to the new inst.
		instName := name
		inst.restartFn = func() error { return c.connect(instName) }
		c.instances.add(inst)
		if err := c.startInstance(c.runCtx, inst); err != nil {
			c.instances.remove(name)
			inst.close()
			return nil, err
		}
		// startInstance falls back to OS stack and returns nil even when
		// both kernel TUN and userspace Nebula failed (e.g. resource
		// still busy). Detect that here so the API caller sees a real
		// error and the retry loop fires.
		if inst.control() == nil {
			c.instances.remove(name)
			inst.close()
			c.servers.shutdownInstance(name)
			return nil, fmt.Errorf("nebula did not start (kernel TUN + userspace both failed; likely 'address already in use' or 'device or resource busy')")
		}
		return inst, nil
	}

	// Multi-attempt retry: linux kernel TUN/UDP release after inst.close()
	// can take several seconds, especially on busy systems or VMs. Total
	// budget ~25 s (4 attempts × 3 s wait + inter-attempt sleeps of 2 s,
	// 4 s, 8 s). Each attempt re-runs the resource waits, so the kernel
	// has more time on each successive try.
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			sleep := time.Duration(1<<uint(attempt)) * time.Second
			log.Printf("[local-api] connect %q attempt %d backoff %v after: %v", name, attempt, sleep, lastErr)
			time.Sleep(sleep)
		}
		_, err := try()
		if err == nil {
			return nil
		}
		lastErr = err
		es := err.Error()
		retryable := strings.Contains(es, "address already in use") ||
			strings.Contains(es, "device or resource busy") ||
			strings.Contains(es, "nebula did not start")
		if !retryable {
			return err
		}
	}

	// Classify the final retry-loop error so the UI can show an
	// actionable message instead of a generic "address already in use"
	// hedge. Most common production case: parallel hop-agent install
	// (system LaunchDaemon left over after .app download, or dev
	// `hop-agent serve` in a terminal) holding the bundled-agent's
	// chosen UDP port.
	es := lastErr.Error()
	if strings.Contains(es, "address already in use") || strings.Contains(es, "device or resource busy") {
		return fmt.Errorf("connect failed after 4 attempts: another hop-agent on this Mac is using the network port. Open Settings → Danger zone → Reset to remove the conflicting install, then try Connect again. (underlying: %s)", es)
	}
	return fmt.Errorf("connect failed after 4 attempts: %w", lastErr)
}

// disconnect is the lifted body of v0.10.34's disconnectFn closure.
// stopwatcher → close → release UDP port + utun + DNS, all inside
// meshInstance.close(). The per-instance HTTP listener is also dropped.
func (c *Client) disconnect(name string) error {
	if c.disconnectOverride != nil {
		return c.disconnectOverride(name)
	}
	inst := c.instances.remove(name)
	if inst == nil {
		return nil // already gone
	}
	inst.close()
	c.servers.shutdownInstance(name)
	return nil
}

// startInstance brings up Nebula + heartbeat + renewal + DNS for one
// enrollment and wires a per-instance HTTP server onto its mesh listener
// (when InstanceHTTPHook is non-nil). Returns an error on hard failures
// (missing token, port bind on the agent listener); soft failures (Nebula
// start) fall back to an OS-stack listener so the renewal loop keeps running.
//
// Lifted from cmd/agent/main.go::tryStartMeshInstance with the mux+server
// wiring abstracted behind c.httpHook (nil = no agent-API listener at all,
// the mode mobile clients use).
func (c *Client) startInstance(ctx context.Context, inst *meshInstance) error {
	cfgPath := filepath.Join(inst.dir(), "nebula.yaml")
	inst.parentCtx = ctx
	// Per-instance ctx so disconnect/leave can stop heartbeat + renewal +
	// path-quality goroutines without taking down the whole agent. v0.10.34
	// invariant.
	inst.runCtx, inst.runCancel = context.WithCancel(ctx)

	authToken, err := readInstanceToken(inst)
	if err != nil {
		return fmt.Errorf("read token: %w", err)
	}

	var handler http.Handler
	if c.httpHook != nil {
		handler = c.httpHook.BuildHandler(inst.name(), authToken)
	}

	pruneOldStuckStateDumps(inst.dir())

	// Start cert renewal + heartbeat regardless of Nebula outcome — even
	// an expired-cert agent needs to renew + re-sync.
	if inst.endpoint() != "" && inst.nodeID() != "" {
		go runCertRenewal(inst.runCtx, inst)
		go runHeartbeat(inst.runCtx, inst)
		// Phase P silent-renewal-death detector + Phase DD silent
		// watchNetworkChanges-death detector. Both fire CRITICAL +
		// forensic dump + restartFn on stamp-age threshold.
		go runRenewalWatchdog(inst.runCtx, inst)
		go runWatcherWatchdog(inst.runCtx, inst)
		log.Printf("[agent %s] cert auto-renewal + heartbeat + watchdogs enabled (endpoint: %s)", inst.name(), inst.endpoint())
	}

	// If nebula.yaml is missing, fall back to OS stack (rare — should only
	// happen if enrollment is corrupt). Renewal might recover it.
	if _, err := os.Stat(cfgPath); err != nil {
		log.Printf("[agent %s] no Nebula config at %s, running on OS stack", inst.name(), cfgPath)
		if handler != nil {
			c.servers.startOSListener(inst, handler, fmt.Sprintf(":%d", agentAPIPort))
		}
		return nil
	}

	tunMode := readTunMode(inst)
	ensureP2PConfig(inst)

	if handler != nil {
		// onRestart: cert-reload's reloadNebula drops the old svc + spawns
		// a new one; the per-instance HTTP listener must rebind onto the
		// new Nebula listener.
		inst.onRestart = func(newSvc meshService) {
			if err := c.servers.rebindMesh(inst, handler, newSvc); err != nil {
				log.Printf("[agent %s] CRITICAL: cannot listen on new Nebula instance: %v", inst.name(), err)
			}
		}
	}

	meshSvc := startMesh(cfgPath, tunMode)
	if meshSvc == nil {
		log.Printf("[agent %s] all Nebula modes failed — falling back to OS stack", inst.name())
		if handler != nil {
			c.servers.startOSListener(inst, handler, fmt.Sprintf(":%d", agentAPIPort))
		}
		return nil
	}
	inst.setSvc(meshSvc)
	log.Printf("[agent %s] Nebula mesh connected (mode: %s)", inst.name(), tunMode)

	// Configure split-DNS for this mesh's domain in kernel TUN mode.
	if tunMode == "kernel" {
		inst.dnsConfig = readDNSConfig(inst)
		configureDNS(inst, inst.dnsConfig)
	}

	// Inject the on-disk peer-endpoint cache into Nebula's hostmap BEFORE
	// any handshake fires. See cmd/agent/main.go pre-extraction notes for
	// the rationale.
	if n := injectCachedPeerEndpoints(inst); n > 0 {
		log.Printf("[agent %s] injected %d cached peer endpoint(s) into hostmap", inst.name(), n)
	}

	// Warm tunnels synchronously: lighthouse first, then peers from
	// heartbeat. Both must complete before the mesh listener starts.
	warmTunnel(cfgPath)
	warmPeersFromHeartbeat(inst, inst.endpoint())

	// Fix D (v0.10.26): mirror the empty-endpoint guard from the reload
	// paths so the watcher startup preconditions are consistent across all
	// entry points.
	if ctrl := meshSvc.NebulaControl(); ctrl != nil && inst.endpoint() != "" {
		inst.startWatcher(ctrl)
	} else if ctrl == nil {
		log.Printf("[agent %s] WARNING: cannot start network-change watcher: no Nebula control", inst.name())
	} else {
		log.Printf("[agent %s] WARNING: cannot start network-change watcher: enrollment endpoint is empty", inst.name())
	}

	if nebulacfg.PortmapEnabled {
		port := inst.enrollment.ListenPort
		if port == 0 {
			port = nebulacfg.ListenPort
		}
		inst.startPortmap(ctx, uint16(port))
	}

	if handler != nil {
		if err := c.servers.startMeshListener(inst, handler, meshSvc, fmt.Sprintf(":%d", agentAPIPort)); err != nil {
			return fmt.Errorf("Nebula mesh listen: %w", err)
		}
		log.Printf("[agent %s] listening on :%d (Nebula mesh, %s TUN)", inst.name(), agentAPIPort, tunMode)
	}

	// Per-peer RTT EWMA via TCP-connect probes to each direct peer's mesh
	// listener. Feeds PeerDetail.RTTms in the heartbeat.
	go runPathQuality(inst.runCtx, inst)

	// CGNAT-aware mesh keepalive. See CLAUDE.md Discovery Log for the
	// rationale on the 90 s cadence.
	go runMeshKeepalive(inst.runCtx, inst)

	// Phase L slice 2: clipboard sync. Per-(device, network) opt-in.
	if inst.enrollment != nil && inst.enrollment.ClipboardSync {
		dir := inst.dir()
		nodeIDBytes, _ := os.ReadFile(filepath.Join(dir, "node-id"))
		tokenBytes, _ := os.ReadFile(filepath.Join(dir, "token"))
		nodeID := strings.TrimSpace(string(nodeIDBytes))
		token := strings.TrimSpace(string(tokenBytes))
		if nodeID != "" && token != "" && inst.endpoint() != "" {
			inst.clipboardSyncRef = startClipboardSync(inst.runCtx, inst, inst.endpoint(), nodeID, token)
		}
	}

	// Layer 4 endpoint-probe DISABLED in v0.10.27.1 hotfix — see
	// CLAUDE.md Discovery Log § "Layer 4 endpoint-probe is DISABLED
	// ENTIRELY in production". Vendor patch infrastructure remains in
	// place for future Layer 4b work.
	// go runEndpointProbe(ctx, inst)

	return nil
}

// RegisterDebugListener serves the supplied http.Handler on the OS network
// stack at the given address. Used by cmd/agent for ad-hoc debug runs
// (--listen, no enrollment) and as a fallback when no enrollments exist.
// Mobile clients have no use for this.
func (c *Client) RegisterDebugListener(handler http.Handler, address string) error {
	return c.servers.startUnscopedOSListener(handler, address)
}

// EnrollmentNames returns the live enrollment names. Used by cmd/agent for
// CLI scaffolding that needs to inspect the registry without exposing the
// internal type.
func (c *Client) EnrollmentNames() []string {
	out := make([]string, 0, c.enrolls.Len())
	for _, e := range c.enrolls.List() {
		out = append(out, e.Name)
	}
	return out
}
