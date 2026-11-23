package client

import (
	"context"
	"log"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/slackhq/nebula"
	"github.com/trustos/hopssh/internal/portmap"
)

// meshInstance is one live Nebula membership owned by the agent. A
// single hop-agent process can hold many of these concurrently — one
// per enrollment. All instance methods are goroutine-safe; the
// enclosing instanceRegistry is what serializes lifecycle transitions
// (add/close) against the running goroutines.
type meshInstance struct {
	enrollment *Enrollment

	svcMu sync.Mutex
	svc   meshService
	// closed is set by close() (under svcMu) and never cleared: a closed
	// instance must not run Nebula again. Anything that can start Nebula
	// in the background (a renewal POST that was in flight, a retry) may
	// finish after close(), and a Nebula started then would be an orphan —
	// in no registry, stoppable by nobody, holding the enrollment's UDP
	// port so every later connect fails with "address already in use".
	closed bool

	// startMu serializes the paths that start Nebula on this instance
	// (startInstance, reloadNebula, scheduleRetryReload). Without it a
	// renewal finishing during startup starts a second Nebula next to the
	// first. Never taken by close(), which must not block behind a wedged
	// start; a start that outlives close() cleans up after itself instead
	// (setSvc returns false).
	startMu sync.Mutex

	// heartbeatTrigger is signaled from watchNetworkChanges and any
	// other out-of-cycle wake-up path so runHeartbeat fires an extra
	// POST. Buffered 1 — a burst of signals coalesces into one heartbeat.
	heartbeatTrigger chan struct{}

	// dnsConfig is the split-DNS configuration registered with the OS
	// for this instance (kernel TUN mode only). Populated at start,
	// cleared on close. dnsMu keeps a start that races with close() from
	// registering DNS after close() cleaned it up.
	dnsMu     sync.Mutex
	dnsConfig *dnsConfig

	// parentCtx is the AGENT'S lifetime (derived from the outer renewCtx).
	// Per-instance work uses lifetimeCtx() instead, so it also ends when
	// this instance is closed.
	parentCtx context.Context

	// runCtx + runCancel scope the per-instance work (heartbeat, cert
	// renewal, path-quality) to the LIFE OF THIS INSTANCE specifically.
	// Cancelled by inst.close() so disconnect/leave correctly stops all
	// per-instance goroutines instead of leaving them running against
	// the agent-wide ctx (the v0.10.34 bug).
	runCtx    context.Context
	runCancel context.CancelFunc

	// watcherCancel stops the currently-running watchNetworkChanges
	// goroutine. Re-derived each time Nebula is (re)started so the
	// watcher always holds a live *nebula.Control reference — a stale
	// one would call RebindUDPServer/CloseAllTunnels on a closed
	// instance, which the Nebula API doesn't contract against. Guarded by
	// watcherMu (not svcMu: stopWatcher runs with svcMu held on reload).
	watcherMu     sync.Mutex
	watcherCancel context.CancelFunc

	// onRestart is invoked after a cert-renewal-driven Nebula restart.
	// The caller wires this to rebind the instance's HTTP listener to
	// the newly-created mesh service.
	onRestart func(meshService)

	// lastMeshErr captures the underlying error from the most recent
	// startMeshWithError call when the mesh failed to bring up. Non-nil
	// only when control() == nil. Read by Client.connect to surface a
	// user-actionable error that distinguishes port-bind failures from
	// cert-clock-skew, network, or config failures (pre-fix the connect
	// retry loop reported "another hop-agent is using the network port"
	// for ALL classes of failure regardless of root cause).
	lastMeshErr error

	// customDir, if non-empty, overrides the default
	// <configDir>/<name> location for this instance's on-disk state.
	// Used by `hop-agent client` for its ephemeral `/etc/hop-client`
	// layout.
	customDir string

	// reloadMu + lastReloadAt throttle automatic Nebula reloads
	// triggered from watchNetworkChanges' "own utun dropped" recovery
	// path. Without a floor, a reload that fails to bring the utun
	// back (transient kernel state) would loop every tick (5 s).
	reloadMu     sync.Mutex
	lastReloadAt time.Time

	// portmap is the UPnP/NAT-PMP/PCP port-mapping coordinator for this
	// instance. Nil if portmap is disabled or no protocol succeeded.
	// Lives for the full instance lifetime; survives Nebula restarts
	// (the mapping targets the stable listen port, not a specific Control).
	// Set after the heartbeat and the watcher, which read it, are running.
	portmap atomic.Pointer[portmap.Manager]

	// pathQuality holds per-peer EWMA RTT samples gathered by
	// runPathQuality; read by collectPeerState to attach RTTms to each
	// PeerDetail in the heartbeat. Set once, in newMeshInstance.
	pathQuality *pathQuality

	// peerInfoCache holds human-readable identifiers for each peer
	// (hostname, auto-generated DNS name, user-defined DNS records),
	// keyed by mesh IP. Refreshed every heartbeat from the server's
	// peerInfo response field. Used by /local/peers to enrich the
	// PeerDetail responses surfaced to the desktop client.
	peerInfoCache sync.Map // string (mesh IP) -> peerInfoEntry

	// clipboardSyncRef, when non-nil, is the per-instance clipboard
	// coordinator (Phase L slice 2). Spawned at startup when
	// enrollment.ClipboardSync == true; lifetime tied to runCtx so
	// disconnect/leave stops it cleanly.
	clipboardSyncRef *clipboardSync

	// Phase P: silent-renewal-death detection.
	//
	// lastRenewalActivityAt is updated atomically at every observable
	// point in the renewal loop (entry, pre-sleep, post-wake, pre-POST,
	// post-POST). The renewal watchdog fires when this stays idle past
	// `renewCertDuration / 4` (= 6h for 24h certs). Source field for
	// the v0.10.79 renewal watchdog mirroring the v0.10.36 stuck-data
	// -plane pattern. Zero value means "no activity recorded yet" —
	// the watchdog skips firing in that case (cold-start grace).
	//
	// lastHeartbeatSuccessAt is updated on each successful heartbeat
	// POST. Used by enrollmentStatus.Connected — the green pill
	// requires either active peer traffic OR a recent successful
	// heartbeat (so the first-startup window where peers haven't
	// handshaken yet doesn't show "disconnected").
	lastRenewalActivityAt  time.Time
	lastHeartbeatSuccessAt time.Time
	// Phase DD (v0.10.96): silent watchNetworkChanges-death detection.
	// Stamped at the TOP of every tick body in watchNetworkChanges. The
	// watcher watchdog fires when this stays idle past
	// watcherSilenceThreshold (= 3 min, 36× the 5s tick + 3× the 60s
	// alive-log cadence). Catches deadlocks inside vendor Nebula's
	// RebindUDPServer / CloseAllTunnels — the third class of silent
	// goroutine failure (renewal + data-plane already covered).
	lastWatcherActivityAt time.Time
	activityMu            sync.Mutex // guards all three fields above

	// restartFn is invoked by the v0.10.36 keepalive watchdog when it
	// detects stuck-data-plane state (consecutive all-failed keepalive
	// cycles). The callback closes the running svc and starts a fresh
	// one. Set by runServe via the connectFn closure so the watchdog
	// has access to runServe-local state (servers, mux, registry).
	// nil means "no auto-recovery wired" — the watchdog logs CRITICAL
	// but cannot self-heal.
	restartFn func() error

	// lastWatchdogRestartAt is the timestamp of the most recent
	// auto-recovery restart triggered by the watchdog. Used to suppress
	// restart-loop cycles: after a recovery, the next 5 minutes of
	// stuck-detection get logged but not acted on, so we don't burn
	// CPU restart-cycling if the underlying issue is persistent.
	lastWatchdogRestartAt time.Time

	// restartNotBefore is when watchdogs may next restart this enrollment.
	// Copied from the Client when the instance is created: a watchdog
	// restart replaces the instance, and with it every cooldown kept on it
	// or in its watchdog goroutines.
	restartNotBefore time.Time

	// watchdogConsecutiveFailures counts how many consecutive times
	// watchdogTrip's restartFn invocation has returned an error.
	// Phase EE (v0.11.24): after escalateAfterN consecutive failures,
	// recordRestartFailure escalates to osExitFn(75) so launchd /
	// systemd / SCM respawns the agent — releases any wedged kernel
	// UDP sockets that restartFn can't clear via inst.close(). Reset
	// to 0 on a successful restart. See watchdog_escalation.go.
	watchdogConsecutiveFailures int

	// meshIPMu + cachedMeshIP + cachedMeshSubnet back the meshIP() and
	// meshSubnet() lazy readers. Cached for the instance lifetime — the
	// cert's VPN IP and subnet don't change across renewals (only the
	// signature/expiry does).
	meshIPMu         sync.Mutex
	cachedMeshIP     string
	cachedMeshSubnet netip.Prefix
}

// reloadCooldown is the minimum spacing between watcher-initiated
// reloads. Exposed as a var so tests can shorten it.
var reloadCooldown = 30 * time.Second

// shouldAutoReload returns true if enough time has passed since the
// last automatic reload (or none has happened). On success it stamps
// lastReloadAt to "now" — callers may proceed to invoke reloadNebula.
func (i *meshInstance) shouldAutoReload() bool {
	i.reloadMu.Lock()
	defer i.reloadMu.Unlock()
	if !i.lastReloadAt.IsZero() && time.Since(i.lastReloadAt) < reloadCooldown {
		return false
	}
	i.lastReloadAt = time.Now()
	return true
}

// newMeshInstance constructs an instance for the given enrollment.
// The returned instance does not have Nebula running yet — the caller
// assigns svc via setSvc once startMesh succeeds.
func newMeshInstance(e *Enrollment) *meshInstance {
	return &meshInstance{
		enrollment:       e,
		heartbeatTrigger: make(chan struct{}, 1),
		pathQuality:      newPathQuality(),
	}
}

// name returns the enrollment name (used for logging). Safe on nil.
func (i *meshInstance) name() string {
	if i == nil || i.enrollment == nil {
		return ""
	}
	return i.enrollment.Name
}

// markRenewalActivity stamps lastRenewalActivityAt to now. Called from
// every observable point in runCertRenewal so the renewal watchdog
// (Phase P) can detect a silent goroutine death.
func (i *meshInstance) markRenewalActivity() {
	if i == nil {
		return
	}
	i.activityMu.Lock()
	i.lastRenewalActivityAt = time.Now()
	i.activityMu.Unlock()
}

// renewalActivityAge returns how long ago renewal last emitted
// activity. Returns time.Duration(math.MaxInt64) when no activity has
// been recorded yet (cold-start grace).
func (i *meshInstance) renewalActivityAge() time.Duration {
	if i == nil {
		return 1<<62 - 1
	}
	i.activityMu.Lock()
	defer i.activityMu.Unlock()
	if i.lastRenewalActivityAt.IsZero() {
		return 1<<62 - 1
	}
	return time.Since(i.lastRenewalActivityAt)
}

// markHeartbeatSuccess stamps lastHeartbeatSuccessAt to now. Called
// from sendHeartbeat on each successful POST. Used by
// enrollmentStatus.Connected to gate the green pill.
func (i *meshInstance) markHeartbeatSuccess() {
	if i == nil {
		return
	}
	i.activityMu.Lock()
	i.lastHeartbeatSuccessAt = time.Now()
	i.activityMu.Unlock()
}

// heartbeatSuccessAge returns how long ago heartbeat last succeeded.
// Returns time.Duration(math.MaxInt64) when no success recorded yet.
func (i *meshInstance) heartbeatSuccessAge() time.Duration {
	if i == nil {
		return 1<<62 - 1
	}
	i.activityMu.Lock()
	defer i.activityMu.Unlock()
	if i.lastHeartbeatSuccessAt.IsZero() {
		return 1<<62 - 1
	}
	return time.Since(i.lastHeartbeatSuccessAt)
}

// markWatcherActivity stamps lastWatcherActivityAt to now. Called at
// the TOP of every tick body in watchNetworkChanges so the watcher
// watchdog (Phase DD) detects silent deadlocks inside vendor Nebula
// calls (RebindUDPServer / CloseAllTunnels).
func (i *meshInstance) markWatcherActivity() {
	if i == nil {
		return
	}
	i.activityMu.Lock()
	i.lastWatcherActivityAt = time.Now()
	i.activityMu.Unlock()
}

// watcherActivityAge returns how long ago the network-change watcher
// last emitted activity. Returns time.Duration(math.MaxInt64) when no
// activity has been recorded yet (cold-start grace).
func (i *meshInstance) watcherActivityAge() time.Duration {
	if i == nil {
		return 1<<62 - 1
	}
	i.activityMu.Lock()
	defer i.activityMu.Unlock()
	if i.lastWatcherActivityAt.IsZero() {
		return 1<<62 - 1
	}
	return time.Since(i.lastWatcherActivityAt)
}

// dir returns the on-disk subdirectory containing this instance's
// cert, token, Nebula yaml, and DNS config. Honors customDir (set by
// hop-agent client for its ephemeral join mode) over the default
// <configDir>/<name> path.
func (i *meshInstance) dir() string {
	if i.customDir != "" {
		return i.customDir
	}
	return enrollmentDir(configDir, i.enrollment.Name)
}

// endpoint returns the enrollment's control plane URL.
func (i *meshInstance) endpoint() string {
	return i.enrollment.Endpoint
}

// nodeID returns the enrollment's server-assigned node id.
func (i *meshInstance) nodeID() string {
	return i.enrollment.NodeID
}

// meshIP returns this enrollment's own VPN/mesh IP as a bare string
// (e.g. "10.42.1.11"), without the netmask. Lazily reads + caches
// from the cert on disk; "" on any failure.
//
// Used by `injectPeerEndpoints` and `injectCachedPeerEndpoints` to
// skip self-loop entries — a defensive filter against any future
// regression that causes the agent's own VPN IP to appear in its
// own peerEndpoints (which would inject self into hostmap, generating
// "Refusing to handshake with myself" log noise on every probe).
func (i *meshInstance) meshIP() string {
	if i == nil {
		return ""
	}
	i.meshIPMu.Lock()
	defer i.meshIPMu.Unlock()
	if i.cachedMeshIP != "" {
		return i.cachedMeshIP
	}
	ip, err := readMeshIPFromCert(filepath.Join(i.dir(), "nebula.yaml"))
	if err != nil {
		return ""
	}
	// Strip CIDR suffix if present (cert returns "10.42.1.11/24").
	if idx := strings.IndexByte(ip, '/'); idx > 0 {
		ip = ip[:idx]
	}
	i.cachedMeshIP = ip
	return i.cachedMeshIP
}

// meshSubnet returns this enrollment's mesh subnet (e.g. 10.42.1.0/24)
// derived from the node certificate. Lazily reads + caches; zero-value
// Prefix on any failure (callers must check IsValid()).
//
// Used by injectPeerEndpoints / injectCachedPeerEndpoints to drop
// any peer-endpoint entry whose VPN address falls OUTSIDE this
// enrollment's subnet — defensive against cross-network endpoint
// distribution. Each network has its own /24 (or whatever prefix the
// cert encodes); a vpnAddr from a different network must not be
// injected into this instance's hostmap.
func (i *meshInstance) meshSubnet() netip.Prefix {
	if i == nil {
		return netip.Prefix{}
	}
	i.meshIPMu.Lock()
	defer i.meshIPMu.Unlock()
	if i.cachedMeshSubnet.IsValid() {
		return i.cachedMeshSubnet
	}
	prefix, err := readMeshSubnetFromCert(filepath.Join(i.dir(), "nebula.yaml"))
	if err != nil {
		return netip.Prefix{}
	}
	i.cachedMeshSubnet = prefix.Masked()
	return i.cachedMeshSubnet
}

// signalHeartbeat asks runHeartbeat (for this instance) to fire one
// out-of-cycle heartbeat. Safe to call from any goroutine; never blocks.
func (i *meshInstance) signalHeartbeat() {
	select {
	case i.heartbeatTrigger <- struct{}{}:
	default:
	}
}

// control returns the current Nebula control, or nil if no mesh
// service is running. Safe to call concurrently with setSvc.
func (i *meshInstance) control() *nebula.Control {
	i.svcMu.Lock()
	defer i.svcMu.Unlock()
	if i.svc == nil {
		return nil
	}
	return i.svc.NebulaControl()
}

// setSvc atomically swaps the mesh service. Does NOT close the old one
// — callers (startup + reload) handle that explicitly so they can
// choose whether to tear down the old listener first.
//
// Returns false, without installing svc, when the instance was closed
// while svc was starting. The caller must then close svc itself.
func (i *meshInstance) setSvc(svc meshService) bool {
	i.svcMu.Lock()
	defer i.svcMu.Unlock()
	if i.closed {
		return false
	}
	i.svc = svc
	return true
}

// isClosed reports whether close() has been called.
func (i *meshInstance) isClosed() bool {
	i.svcMu.Lock()
	defer i.svcMu.Unlock()
	return i.closed
}

// restartCoolingDown reports whether watchdog restarts are on hold because
// the enrollment was restarted recently.
func (i *meshInstance) restartCoolingDown() bool {
	return time.Now().Before(i.restartNotBefore)
}

// lifetimeCtx ends when this instance is closed or the agent stops. Falls
// back to parentCtx / Background for instances that never ran startInstance
// (pre-start renewal, `hop-agent client`, tests).
func (i *meshInstance) lifetimeCtx() context.Context {
	if i.runCtx != nil {
		return i.runCtx
	}
	if i.parentCtx != nil {
		return i.parentCtx
	}
	return context.Background()
}

// configureSplitDNS registers the mesh domain with the OS resolver. No-op
// once the instance is closed.
func (i *meshInstance) configureSplitDNS() {
	i.dnsMu.Lock()
	defer i.dnsMu.Unlock()
	if i.isClosed() {
		return
	}
	i.dnsConfig = readDNSConfig(i)
	configureDNS(i, i.dnsConfig)
}

// cleanupSplitDNS undoes configureSplitDNS. Idempotent.
func (i *meshInstance) cleanupSplitDNS() {
	i.dnsMu.Lock()
	defer i.dnsMu.Unlock()
	if i.dnsConfig != nil {
		cleanupDNS(i, i.dnsConfig)
		i.dnsConfig = nil
	}
}

// currentSvc returns the running mesh service, or nil if Nebula hasn't
// started (or has stopped).
func (i *meshInstance) currentSvc() meshService {
	i.svcMu.Lock()
	defer i.svcMu.Unlock()
	return i.svc
}

// startWatcher spawns watchNetworkChanges under a fresh context
// anchored on the instance's lifetime: it survives cert-renewal reloads
// (which don't close the instance) and ends with close(). Any prior
// watcher is cancelled first so the new one is the only live goroutine
// holding the current ctrl. Called at initial startup and after every
// cert-renewal reload.
func (i *meshInstance) startWatcher(ctrl *nebula.Control) {
	i.watcherMu.Lock()
	defer i.watcherMu.Unlock()
	if i.watcherCancel != nil {
		i.watcherCancel()
	}
	ctx, cancel := context.WithCancel(i.lifetimeCtx())
	i.watcherCancel = cancel
	go watchNetworkChanges(ctx, i, ctrl)
}

// stopWatcher cancels the current watchNetworkChanges goroutine, if
// any. Idempotent.
func (i *meshInstance) stopWatcher() {
	i.watcherMu.Lock()
	defer i.watcherMu.Unlock()
	if i.watcherCancel != nil {
		i.watcherCancel()
		i.watcherCancel = nil
	}
}

// startPortmap brings up the port-mapping coordinator for this instance.
// Idempotent; safe to call multiple times (subsequent calls no-op). Must
// be called AFTER the first setSvc so the OnChange callback can reach
// the live *nebula.Control.
//
// When a public mapping lands, the callback injects it into the current
// Control's lighthouse.advertise_addrs via the patch-11 API. If Nebula
// later restarts (cert renewal), reinjectPortmapAddr re-adds the mapping
// to the new Control.
func (i *meshInstance) startPortmap(ctx context.Context, listenPort uint16) {
	if i.portmap.Load() != nil {
		return
	}
	pm := portmap.New(nil, listenPort)
	pm.OnChange(func(old, cur netip.AddrPort) {
		ctrl := i.control()
		if ctrl == nil {
			return
		}
		if old.IsValid() {
			ctrl.RemoveAdvertiseAddr(old)
		}
		if cur.IsValid() {
			ctrl.AddAdvertiseAddr(cur)
		}
	})
	if err := pm.Start(ctx); err != nil {
		log.Printf("[agent %s] portmap: start: %v", i.name(), err)
		return
	}
	i.portmap.Store(pm)
}

// reinjectPortmapAddr re-adds the current portmap mapping to a freshly-
// started Nebula Control (called after cert-renewal reload). No-op if
// no mapping exists.
func (i *meshInstance) reinjectPortmapAddr() {
	pm := i.portmap.Load()
	if pm == nil {
		return
	}
	cur := pm.Current()
	if !cur.IsValid() {
		return
	}
	if ctrl := i.control(); ctrl != nil {
		ctrl.AddAdvertiseAddr(cur)
	}
}

// stopPortmap tears down the portmap coordinator, best-effort-unmapping
// on the router. Idempotent.
func (i *meshInstance) stopPortmap() {
	if pm := i.portmap.Swap(nil); pm != nil {
		pm.Stop()
	}
}

// close tears down the instance: stops goroutines, closes Nebula,
// cleans up DNS. Idempotent. Safe to call from any goroutine.
//
// Order matters: cancel runCtx FIRST so per-instance goroutines
// (heartbeat, renewal, path-quality) observe Done() and exit before
// we touch the underlying Nebula svc. Otherwise a heartbeat in flight
// could panic on inst.svc dereference.
//
// Cancelling alone is not enough: a renewal past its POST, or a Nebula
// start already under way, won't notice ctx. Setting closed under svcMu
// is what stops those from leaving Nebula running afterwards (see setSvc).
func (i *meshInstance) close() {
	i.svcMu.Lock()
	i.closed = true
	if i.runCancel != nil {
		i.runCancel()
		i.runCancel = nil
	}
	i.svcMu.Unlock()
	i.stopWatcher()
	i.stopPortmap()
	i.svcMu.Lock()
	svc := i.svc
	i.svc = nil
	i.svcMu.Unlock()
	if svc != nil {
		svc.Close()
	}
	i.cleanupSplitDNS()
}

// instanceRegistry tracks live mesh instances keyed by enrollment name.
// Phase B: runServe populates it at boot and closeAll on shutdown.
// Phase C+: hop-agent leave/enroll will mutate it at runtime.
type instanceRegistry struct {
	mu     sync.RWMutex
	byName map[string]*meshInstance
}

func newInstanceRegistry() *instanceRegistry {
	return &instanceRegistry{byName: make(map[string]*meshInstance)}
}

// add inserts an instance. Caller is responsible for ensuring the name
// isn't already present (enrollment registry's uniqueness guarantees this
// at boot).
func (r *instanceRegistry) add(inst *meshInstance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byName[inst.enrollment.Name] = inst
}

// get returns the instance with the given name, or nil.
func (r *instanceRegistry) get(name string) *meshInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byName[name]
}

// list returns a snapshot of live instances. Iteration order is
// undefined (use the enrollmentRegistry's Names() if you need sorted).
func (r *instanceRegistry) list() []*meshInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*meshInstance, 0, len(r.byName))
	for _, inst := range r.byName {
		out = append(out, inst)
	}
	return out
}

// len returns the number of live instances.
func (r *instanceRegistry) len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byName)
}

// remove drops the named instance from the registry and returns it
// (without closing). The caller is responsible for inst.close(). This
// split — drop-from-registry vs close-instance — lets the local API's
// disconnect handler atomically prune the registry while running the
// (potentially blocking) close outside any registry lock.
//
// Returns nil if no instance with that name exists.
func (r *instanceRegistry) remove(name string) *meshInstance {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, ok := r.byName[name]
	if !ok {
		return nil
	}
	delete(r.byName, name)
	return inst
}

// closeAll tears down every instance. Safe to call from the shutdown
// path. Close happens outside the registry lock so a blocking close
// can't deadlock concurrent reads.
func (r *instanceRegistry) closeAll() {
	r.mu.Lock()
	instances := make([]*meshInstance, 0, len(r.byName))
	for _, inst := range r.byName {
		instances = append(instances, inst)
	}
	r.byName = make(map[string]*meshInstance)
	r.mu.Unlock()

	for _, inst := range instances {
		log.Printf("[agent] stopping instance %q", inst.name())
		inst.close()
	}
}
