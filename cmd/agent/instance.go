package main

import (
	"context"
	"log"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
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

	// heartbeatTrigger is signaled from watchNetworkChanges and any
	// other out-of-cycle wake-up path so runHeartbeat fires an extra
	// POST. Buffered 1 — a burst of signals coalesces into one heartbeat.
	heartbeatTrigger chan struct{}

	// dnsConfig is the split-DNS configuration registered with the OS
	// for this instance (kernel TUN mode only). Populated at start,
	// cleared on close.
	dnsConfig *dnsConfig

	// parentCtx scopes every per-instance goroutine (heartbeat,
	// renewal, network-change watcher) to the agent's lifetime.
	// Assigned once in startMeshInstance.
	parentCtx context.Context

	// watcherCancel stops the currently-running watchNetworkChanges
	// goroutine. Re-derived each time Nebula is (re)started so the
	// watcher always holds a live *nebula.Control reference — a stale
	// one would call RebindUDPServer/CloseAllTunnels on a closed
	// instance, which the Nebula API doesn't contract against.
	watcherCancel context.CancelFunc

	// onRestart is invoked after a cert-renewal-driven Nebula restart.
	// The caller wires this to rebind the instance's HTTP listener to
	// the newly-created mesh service.
	onRestart func(meshService)

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
	portmap *portmap.Manager

	// pathQuality holds per-peer EWMA RTT samples gathered by
	// runPathQuality. Populated lazily on instance startup; read by
	// collectPeerState to attach RTTms to each PeerDetail in the
	// heartbeat. Nil-safe via pathQuality.snapshot().
	pathQuality *pathQuality

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
	}
}

// name returns the enrollment name (used for logging). Safe on nil.
func (i *meshInstance) name() string {
	if i == nil || i.enrollment == nil {
		return ""
	}
	return i.enrollment.Name
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
func (i *meshInstance) setSvc(svc meshService) {
	i.svcMu.Lock()
	defer i.svcMu.Unlock()
	i.svc = svc
}

// currentSvc returns the running mesh service, or nil if Nebula hasn't
// started (or has stopped).
func (i *meshInstance) currentSvc() meshService {
	i.svcMu.Lock()
	defer i.svcMu.Unlock()
	return i.svc
}

// startWatcher spawns watchNetworkChanges under a fresh context
// anchored on i.parentCtx. Any prior watcher is cancelled first so
// the new one is the only live goroutine holding the current ctrl.
// Called at initial startup and after every cert-renewal reload.
func (i *meshInstance) startWatcher(ctrl *nebula.Control) {
	if i.watcherCancel != nil {
		i.watcherCancel()
	}
	parent := i.parentCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	i.watcherCancel = cancel
	go watchNetworkChanges(ctx, i, ctrl)
}

// stopWatcher cancels the current watchNetworkChanges goroutine, if
// any. Idempotent.
func (i *meshInstance) stopWatcher() {
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
	if i.portmap != nil {
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
	i.portmap = pm
}

// reinjectPortmapAddr re-adds the current portmap mapping to a freshly-
// started Nebula Control (called after cert-renewal reload). No-op if
// no mapping exists.
func (i *meshInstance) reinjectPortmapAddr() {
	if i.portmap == nil {
		return
	}
	cur := i.portmap.Current()
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
	if i.portmap == nil {
		return
	}
	i.portmap.Stop()
	i.portmap = nil
}

// close tears down the instance: stops goroutines, closes Nebula,
// cleans up DNS. Idempotent. Safe to call from any goroutine.
func (i *meshInstance) close() {
	i.stopWatcher()
	i.stopPortmap()
	i.svcMu.Lock()
	svc := i.svc
	i.svc = nil
	i.svcMu.Unlock()
	if svc != nil {
		svc.Close()
	}
	if i.dnsConfig != nil {
		cleanupDNS(i, i.dnsConfig)
		i.dnsConfig = nil
	}
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
