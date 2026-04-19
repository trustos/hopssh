package main

import (
	"context"
	"log"
	"sync"

	"github.com/slackhq/nebula"
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

	// cancel stops the per-instance goroutines (heartbeat, renewal,
	// network-change watcher) when the instance is closing down.
	cancel context.CancelFunc

	// onRestart is invoked after a cert-renewal-driven Nebula restart.
	// The caller wires this to rebind the instance's HTTP listener to
	// the newly-created mesh service.
	onRestart func(meshService)

	// customDir, if non-empty, overrides the default
	// <configDir>/<name> location for this instance's on-disk state.
	// Used by `hop-agent client` for its ephemeral `/etc/hop-client`
	// layout.
	customDir string
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

// close tears down the instance: stops goroutines, closes Nebula,
// cleans up DNS. Idempotent. Safe to call from any goroutine.
func (i *meshInstance) close() {
	if i.cancel != nil {
		i.cancel()
	}
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
