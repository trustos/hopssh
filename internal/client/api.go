// Package client is the shared agent-lifecycle substrate consumed by hop-agent
// (cmd/agent) and any future embedded clients (iOS NEPacketTunnelProvider,
// Android VpnService via gomobile, Rust-FFI etc.).
//
// The exported API surface is constrained to gomobile-compatible types so the
// same package can be bound for mobile without a separate "v2" surface:
//
//   - No chan / func / interface{} fields in exported structs.
//   - All exported error returns are plain error.
//   - All paths are string.
//   - All times are time.Time / time.Duration.
//
// Regression-tested via api_constraints_test.go.
package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Config holds all paths + tunables. No callbacks here — events flow via
// Subscribe / SubscribeCallback. ConfigDir is the on-disk root that contains
// enrollments.json + per-enrollment subdirs.
type Config struct {
	ConfigDir         string
	LogWriter         io.Writer
	UserAgent         string
	HeartbeatInterval time.Duration
}

// Client is the top-level handle. One per process.
type Client struct {
	cfg Config

	enrolls   *enrollmentRegistry
	instances *instanceRegistry
	servers   *serverSet

	mu        sync.Mutex
	runCtx    context.Context
	runCancel context.CancelFunc
	started   atomic.Bool

	httpHook InstanceHTTPHook

	// connectOverride / disconnectOverride let tests intercept the
	// connect/disconnect lifecycle without spinning up a real mesh.
	// Both fields are UNEXPORTED so they don't violate the FFI
	// constraint rules in api_constraints_test.go (only exported
	// fields are scanned).
	connectOverride    func(name string) error
	disconnectOverride func(name string) error

	subMu    sync.Mutex
	nextSub  uint64
	subChans map[uint64]chan Event
	subCBs   map[uint64]EventCallback

	// bootErrors records the most recent Client.connect failure per
	// enrollment, indexed by enrollment name. Phase GG (v0.11.26):
	// surfaces via /local/status.EnrollmentStatus.LastError so the
	// .app + dashboard can show WHICH enrollment failed and WHY
	// (cert expired, port collision, clock skew) instead of the
	// user just observing a tight launchd respawn loop with no
	// actionable info. Recorded by Start(); cleared on a successful
	// reconnect via Connect() / connect.
	bootErrorsMu sync.Mutex
	bootErrors   map[string]string
}

// NewClient validates Config + ConfigDir, loads the enrollment registry. Does
// NOT start any networks.
//
// Caller is responsible for running MigrateLegacyLayout(ConfigDir) BEFORE
// invoking NewClient — see migrate_legacy.go. NewClient assumes the registry
// is already in the per-enrollment subdir layout introduced in v0.10.
func NewClient(cfg Config) (*Client, error) {
	if cfg.ConfigDir == "" {
		return nil, errors.New("client: Config.ConfigDir is required")
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = 60 * time.Second
	}
	// Override the package-level configDir var (legacy global used by
	// enroll.go / enrollments.go internals) so subsequent operations
	// scope to the caller's chosen path. Keeping the var for now to
	// minimise the M2 diff; M3+ can consolidate.
	configDir = cfg.ConfigDir
	reg, err := loadEnrollmentRegistry(cfg.ConfigDir)
	if err != nil {
		return nil, err
	}
	return &Client{
		cfg:       cfg,
		enrolls:   reg,
		instances: newInstanceRegistry(),
		servers:   newServerSet(),
		subChans:  map[uint64]chan Event{},
		subCBs:    map[uint64]EventCallback{},
	}, nil
}

// SetInstanceHTTPHook wires a per-instance HTTP listener bridge from the
// caller (cmd/agent supplies the agent-API mux; mobile supplies nil). Must be
// called before Start. Calling with nil is the explicit "no mesh-API listener"
// mode that mobile clients need.
func (c *Client) SetInstanceHTTPHook(hook InstanceHTTPHook) {
	c.httpHook = hook
}

// Start spawns goroutines for all live enrollments + the three watchdogs.
// Idempotent: calling Start when already started is a no-op.
func (c *Client) Start(ctx context.Context) error {
	if !c.started.CompareAndSwap(false, true) {
		return nil
	}
	c.mu.Lock()
	c.runCtx, c.runCancel = context.WithCancel(ctx)
	c.mu.Unlock()

	// Self-heal listen ports + nebula.yaml drift before any instance comes up.
	migrateListenPorts(c.enrolls)

	// Boot-time clock-sanity gate. EnsureClockSane against the first
	// enrollment's control plane HTTPS Date header; on drift, attempt
	// platform-native NTP resync before any Nebula handshake. Closes the
	// UTM-suspended-VM / no-RTC-board class of "every handshake fails
	// with certificate is expired" failures.
	if list := c.enrolls.List(); len(list) > 0 {
		EnsureClockSane(c.runCtx, list[0].Endpoint)
	}

	// Phase GG (v0.11.26): per-enrollment connect failures are non-fatal
	// to Start. Pre-fix, a single bad enrollment (expired cert, port
	// conflict, clock skew) caused Start to return error → main.go
	// log.Fatalf → process exit → launchd respawn → tight ~15s restart
	// loop that took down EVERY enrollment along with the broken one AND
	// prevented client.StartLocalAPI from ever running, leaving the
	// .app's mirror token file stale at its last successful boot
	// (observed live: MBP mirror file last updated May 14, agent
	// restarting every 15s for hours). Now: log each per-enrollment
	// failure, record it for surfacing via /local/status's LastError
	// field, continue with the remaining enrollments. Start returns
	// error ONLY if every enrollment failed.
	var attempted, failed int
	for _, e := range c.enrolls.List() {
		attempted++
		if err := c.connect(e.Name); err != nil {
			log.Printf("[client] enrollment %q connect failed (agent continues; see /local/status lastError): %v",
				e.Name, err)
			c.recordBootError(e.Name, err)
			failed++
			continue
		}
		c.clearBootError(e.Name)
	}
	if attempted > 0 && failed == attempted {
		return fmt.Errorf("all %d enrollment(s) failed to boot — see per-enrollment lastError in /local/status", failed)
	}
	if failed > 0 {
		log.Printf("[client] %d of %d enrollment(s) failed to boot — agent continues with the working ones",
			failed, attempted)
	}
	return nil
}

// Stop tears down all instances, cancels watchdogs, returns when fully drained.
func (c *Client) Stop() error {
	if !c.started.CompareAndSwap(true, false) {
		return nil
	}
	c.mu.Lock()
	cancel := c.runCancel
	c.runCancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.servers.shutdownAll()
	c.instances.closeAll()
	c.closeSubscribers()
	return nil
}

// Connect brings up a single named enrollment. Idempotent. Returns nil if
// already connected. Public method form of v0.10.34's connectFn closure.
func (c *Client) Connect(ctx context.Context, name string) error {
	_ = ctx // current connect path uses c.runCtx; future per-call ctx may swap
	err := c.connect(name)
	if err != nil {
		c.recordBootError(name, err)
	} else {
		c.clearBootError(name)
	}
	return err
}

// recordBootError stores the most recent Client.connect failure for an
// enrollment so /local/status can surface it via EnrollmentStatus.LastError.
// Phase GG (v0.11.26): replaces the old "agent restart-loops with no
// actionable error" UX with "agent stays alive, .app shows which
// enrollment failed and why".
func (c *Client) recordBootError(name string, err error) {
	if err == nil {
		return
	}
	c.bootErrorsMu.Lock()
	defer c.bootErrorsMu.Unlock()
	if c.bootErrors == nil {
		c.bootErrors = map[string]string{}
	}
	c.bootErrors[name] = err.Error()
}

// clearBootError removes a stored boot error after a successful reconnect.
// Idempotent — safe to call when no error is recorded.
func (c *Client) clearBootError(name string) {
	c.bootErrorsMu.Lock()
	defer c.bootErrorsMu.Unlock()
	if c.bootErrors == nil {
		return
	}
	delete(c.bootErrors, name)
}

// LastBootError returns the most recent Client.connect failure for an
// enrollment, or "" when none. Exported so the local-API status handler
// can surface it via EnrollmentStatus.LastError, and so the desktop
// client + dashboard can show actionable per-enrollment errors instead
// of forcing the user to dig through log files.
func (c *Client) LastBootError(name string) string {
	c.bootErrorsMu.Lock()
	defer c.bootErrorsMu.Unlock()
	return c.bootErrors[name]
}

// Disconnect tears down a single named enrollment. Idempotent. Public method
// form of v0.10.34's disconnectFn closure.
func (c *Client) Disconnect(ctx context.Context, name string) error {
	_ = ctx
	return c.disconnect(name)
}

// Enroll runs the device-flow / token / bundle enrollment against
// opts.Endpoint, persists the new enrollment to disk, and (if Started) brings
// its instance online. Returns the new enrollment name.
//
// M2 stub: the underlying enroll.go entrypoints (runDeviceFlow, runTokenEnroll,
// runBundleEnroll) are CLI-shaped (parse-and-exit) and need adaptation to
// return the name + error pair this method exposes. Adaptation lands in M3
// alongside the cmd/agent rewire.
func (c *Client) Enroll(ctx context.Context, opts EnrollOptions) (string, error) {
	return "", errors.New("client.Enroll: not yet wired (M3)")
}

// Leave disconnects + removes a network. Idempotent. M2 stub — full impl in M3.
func (c *Client) Leave(ctx context.Context, name string) error {
	if err := c.Disconnect(ctx, name); err != nil {
		return err
	}
	if err := c.enrolls.Remove(name); err != nil {
		return err
	}
	return nil
}

// Status returns a snapshot of all enrollments + their connection state.
// JSON-friendly types only — no chan / func / interface{} fields.
func (c *Client) Status() Snapshot {
	out := Snapshot{CapturedAt: time.Now()}
	for _, e := range c.enrolls.List() {
		es := EnrollmentSummary{
			Name:     e.Name,
			Endpoint: e.Endpoint,
		}
		if inst := c.instances.get(e.Name); inst != nil && inst.control() != nil {
			es.Connected = true
		}
		out.Enrollments = append(out.Enrollments, es)
	}
	return out
}

// Peers returns the peer list for a single enrollment. M2 stub — wires to
// peerstate.go in M3.
func (c *Client) Peers(name string) ([]PeerInfo, error) {
	if c.instances.get(name) == nil {
		return nil, errors.New("client.Peers: enrollment not connected")
	}
	// Detail unwiring lives in M3.
	return nil, nil
}

// ForceRenew triggers an inline cert renewal for a single enrollment. M2 stub
// — wires to renew.go::renewCert in M3.
func (c *Client) ForceRenew(ctx context.Context, name string) error {
	_ = ctx
	inst := c.instances.get(name)
	if inst == nil {
		return errors.New("client.ForceRenew: enrollment not connected")
	}
	return renewCert(inst)
}

// Subscribe returns a channel of events for in-process Go consumers. The
// channel is closed on Stop.
//
// FFI consumers (gomobile, Rust) cannot cross the binding boundary with chan
// — they should use SubscribeCallback instead. Methods returning channels
// don't violate the FFI constraints (only struct fields do).
func (c *Client) Subscribe() <-chan Event {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	id := c.nextSub
	c.nextSub++
	ch := make(chan Event, 32)
	c.subChans[id] = ch
	return ch
}

// SubscribeCallback registers an event callback. Returns a SubscriptionID
// that can be passed to Unsubscribe. FFI-safe.
func (c *Client) SubscribeCallback(cb EventCallback) SubscriptionID {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	id := c.nextSub
	c.nextSub++
	c.subCBs[id] = cb
	return SubscriptionID{id: id}
}

// Unsubscribe removes a previously-registered callback or channel.
func (c *Client) Unsubscribe(id SubscriptionID) {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	if ch, ok := c.subChans[id.id]; ok {
		delete(c.subChans, id.id)
		close(ch)
	}
	delete(c.subCBs, id.id)
}

func (c *Client) closeSubscribers() {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	for id, ch := range c.subChans {
		delete(c.subChans, id)
		close(ch)
	}
	c.subCBs = map[uint64]EventCallback{}
}

// EnrollOptions configures Client.Enroll. Exactly one of Token / DeviceFlow /
// BundlePath should be set.
type EnrollOptions struct {
	Endpoint    string        // hopssh control-plane URL
	Name        string        // optional local name; auto-generated if empty
	Token       string        // single-use enrollment token from dashboard
	DeviceFlow  bool          // run interactive device-flow auth
	BundlePath  string        // path to a pre-issued bundle .zip
	PollTimeout time.Duration // device-flow only; default 5min
}

// Snapshot is a JSON-friendly view of all enrollments + their state.
type Snapshot struct {
	Enrollments []EnrollmentSummary
	CapturedAt  time.Time
}

// EnrollmentSummary describes one enrollment + its current connectivity.
//
// Distinct from local_api.go's EnrollmentStatus, which is the dashboard-
// shaped JSON wire format used by the desktop client's /local/status
// endpoint and carries display-string time fields. This summary is the
// FFI-friendly view exposed to mobile / Rust consumers.
type EnrollmentSummary struct {
	Name             string
	Endpoint         string
	NetworkID        string
	NodeID           string
	MeshIP           string
	Connected        bool
	CertNotAfter     time.Time
	LastHeartbeatAt  time.Time
	PeerCount        int
	RunMode          string // "kernel-tun" / "userspace" / "os-stack"
	TunMode          string // "kernel" / "userspace"
	WatchdogTrips    int
	LastWatchdogTrip time.Time
}

// PeerInfo describes one mesh peer for an enrollment.
type PeerInfo struct {
	NodeID       string
	Hostname     string
	MeshIP       string
	Direct       bool
	RelayedVia   string
	LastSeen     time.Time
	IsLighthouse bool
	OSName       string
	OSArch       string
	AgentVersion string
}

// Event is one entry in the Subscribe / SubscribeCallback stream.
type Event struct {
	At         time.Time
	Type       string // "cert.renewed", "watchdog.tripped", "peer.online" etc.
	Enrollment string // empty for client-wide events
	Severity   string // "info" / "warn" / "critical"
	Message    string
}

// EventCallback is invoked for each Event delivered to a subscriber that
// registered via SubscribeCallback. FFI-safe (interface argument, not a
// struct field).
type EventCallback interface {
	OnEvent(e Event)
}

// SubscriptionID is the handle returned by SubscribeCallback or implicitly by
// Subscribe. Pass to Unsubscribe to remove the registration.
type SubscriptionID struct {
	id uint64
}

// InstanceHTTPHook lets the caller (cmd/agent) attach a stateless HTTP
// handler to each per-instance mesh listener. Mobile clients pass nil to
// skip the agent-API listener entirely.
//
// BuildHandler is called once per instance bring-up. The returned handler is
// served on this instance's mesh listener (and the OS-stack fallback if
// Nebula fails). Implementations typically wrap a stateless mux with
// bearer-token auth.
type InstanceHTTPHook interface {
	BuildHandler(name string, authToken string) http.Handler
}

// MigrateLegacyLayout is the exported wrapper around legacy_migrate.go's
// internal migrateLegacyLayout. cmd/agent calls this BEFORE NewClient so the
// registry is in the per-enrollment subdir layout NewClient expects.
func MigrateLegacyLayout(configDir string) error {
	_, err := migrateLegacyLayout(configDir)
	return err
}

// AgentAPIPort is the TCP port the agent's per-instance HTTP listener binds
// to on the mesh IP (and the OS-stack fallback). Exported so cmd/agent can
// reference the same constant without a separate definition.
const AgentAPIPort = agentAPIPort

// ResolveConfigDir is the exported wrapper around enroll.go's internal
// resolveConfigDir. cmd/agent uses this to reconcile the --config-dir flag
// against platform defaults before constructing a Client.
func ResolveConfigDir(override string) string { return resolveConfigDir(override) }

// SetSystemMirrorDirOverride configures the system-mode mirror directory
// (the .app's well-known dir for token + port discovery without root
// access). cmd/agent sets this from the --mirror-dir flag.
func SetSystemMirrorDirOverride(dir string) { systemMirrorDirOverride = dir }

// RunMirrorChownSelfHeal kicks off the Phase Z self-heal goroutine that
// periodically re-chowns mirror files if they're still root-owned. Returns
// immediately; the goroutine runs until ctx is cancelled. Safe to call
// even when systemMirrorDirOverride is empty (it short-circuits internally).
func RunMirrorChownSelfHeal(ctx context.Context) {
	go runMirrorChownSelfHeal(ctx, systemMirrorDirOverride)
}

// SvcIntegrateIfNeeded is the exported wrapper for the platform-specific
// Windows SCM integration (no-op on darwin/linux). cmd/agent calls this
// at the top of runServe.
func SvcIntegrateIfNeeded(cancel context.CancelFunc) bool {
	return svcIntegrateIfNeeded(cancel)
}

// CleanupOldBinary removes a leftover <exe>.old from a previous Windows
// self-update. No-op on other platforms.
func CleanupOldBinary() { cleanupOldBinary() }

// StartPprofIfRequested activates the loopback pprof listener if the
// HOPSSH_PPROF_ADDR env var is set. cmd/agent calls this at startup.
func StartPprofIfRequested() { startPprofIfRequested() }

// RunMigration is the exported entry point for the `hop-agent migration`
// QUIC connection-migration probe subcommand. Phase NN: previously a
// cmd/agent-local function; tunneling and quictransport already live in
// internal/, so the probe lifted cleanly.
//
// Hosts the function signature even though migration.go itself stayed in
// cmd/agent — runs against the published quictransport import path.
// Removed: this stub is unused; migration.go in cmd/agent calls
// quictransport.RunProbe directly.
