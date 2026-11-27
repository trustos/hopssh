package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/slackhq/nebula/cert"
	"github.com/trustos/hopssh/internal/buildinfo"
	"github.com/trustos/hopssh/internal/nebulacfg"
	"gopkg.in/yaml.v3"
)

// errNodeRevoked is returned (wrapped) when the control plane answers 401
// on /api/heartbeat or /api/renew: the node was deleted or its agent token
// revoked. Retrying can never succeed, so the cert-recovery loop stops on it.
var errNodeRevoked = errors.New("401: node deleted or token revoked")

// errRateLimited is returned (wrapped) when the control plane answers 429.
// Callers must back off for at least rateLimitedMinRetry — a fast retry
// only re-spends the same exhausted per-IP bucket.
var errRateLimited = errors.New("HTTP 429: rate limited by control plane")

// errInstanceClosed is returned when Nebula finished starting on an instance
// that was closed meanwhile (disconnect, watchdog restart, shutdown). The new
// Nebula is stopped again.
var errInstanceClosed = errors.New("instance was closed while Nebula was starting")

// rateLimitedMinRetry is the minimum wait after a 429 from the control plane.
const rateLimitedMinRetry = 60 * time.Second

// nextHeartbeatRetry clamps a heartbeat retry interval after a failure:
// a 429 never retries sooner than rateLimitedMinRetry.
func nextHeartbeatRetry(interval time.Duration, err error) time.Duration {
	if errors.Is(err, errRateLimited) && interval < rateLimitedMinRetry {
		return rateLimitedMinRetry
	}
	return interval
}

// runHeartbeat sends periodic heartbeats to the control plane so the
// dashboard shows the node as "online".
//
// Normal mode: every 5 minutes. On failure: fast retry with exponential
// backoff (5s → 10s → 20s → ... → 2m cap) so agents recover within seconds
// of a server redeploy. All intervals include ±10% jitter to prevent
// thundering herd across agents.
//
// One goroutine is started per meshInstance; each runs independently
// with its own heartbeat cadence, backoff state, and wake channel.
func runHeartbeat(ctx context.Context, inst *meshInstance) {
	const (
		normalInterval = 60 * time.Second
		initialRetry   = 5 * time.Second
		maxRetry       = 2 * time.Minute
	)

	// Send initial heartbeat immediately.
	initialErr := sendHeartbeat(inst)
	failing := initialErr != nil
	retryInterval := initialRetry

	next := normalInterval
	if failing {
		next = nextHeartbeatRetry(initialRetry, initialErr)
	}

	timer := time.NewTimer(addJitter(next))
	defer timer.Stop()

	// fire sends one heartbeat and schedules the next tick based on
	// success/failure. Shared between the scheduled-timer path and the
	// wake-triggered out-of-cycle path so both obey the same
	// backoff/recovery state machine.
	fire := func() {
		err := sendHeartbeat(inst)
		if err != nil {
			if !failing {
				failing = true
				retryInterval = initialRetry
				log.Printf("[heartbeat %s] failed, switching to fast retry: %v", inst.name(), err)
			} else {
				retryInterval *= 2
				if retryInterval > maxRetry {
					retryInterval = maxRetry
				}
			}
			retryInterval = nextHeartbeatRetry(retryInterval, err)
			timer.Reset(addJitter(retryInterval))
		} else {
			if failing {
				log.Printf("[heartbeat %s] recovered after retry", inst.name())
				failing = false
				retryInterval = initialRetry
			}
			timer.Reset(addJitter(normalInterval))
		}
	}

	// drainTimer stops the scheduled timer and drains any pending tick,
	// so an out-of-cycle fire doesn't race with a scheduled one.
	drainTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			fire()
		case <-inst.heartbeatTrigger:
			log.Printf("[heartbeat %s] wake/network-change triggered out-of-cycle heartbeat", inst.name())
			drainTimer()
			fire()
		}
	}
}

func sendHeartbeat(inst *meshInstance) error {
	// Build the heartbeat body. Include peer counts + per-peer detail
	// when Nebula control is available (both kernel-TUN and userspace
	// modes expose it). Omit the peer fields when unavailable — the
	// server preserves the last known good values via COALESCE rather
	// than overwriting with zeros/empty.
	//
	// Multi-network-per-agent (roadmap #29): each instance fires its
	// own POSTs independently with its own nodeID + token. Body schema
	// stays singular; the control plane never sees cross-instance data.
	reqBody := map[string]any{
		"nodeId":       inst.nodeID(),
		"agentVersion": buildinfo.Version, // "vX.Y.Z" (tagged) or "vX.Y.Z-N-gSHORTSHA(-dirty)" (dev)
		// The machine's own OS and arch, from the runtime running this
		// code. The node row keeps what enrollment wrote, and
		// enrollment guesses: the token body said "linux" for every
		// machine until 72f2165, which is not in a release yet. This is
		// where the truth arrives, and where an old node is corrected.
		"os":   runtime.GOOS,
		"arch": detectArch(),
	}
	// ClientType is build-baked (-X main.clientType=...) so the dashboard
	// can show "Desktop" vs "CLI" provenance per node. Skip when empty
	// (legacy/dev builds) — the server treats absent as unknown.
	if buildinfo.ClientType != "" {
		reqBody["clientType"] = buildinfo.ClientType
	}
	if direct, relayed, peers, ok := collectPeerState(inst.control(), inst.pathQuality); ok {
		reqBody["peersDirect"] = direct
		reqBody["peersRelayed"] = relayed
		if len(peers) > 0 {
			reqBody["peers"] = peers
		}
	}
	// Phase G: report this agent's own observed UDP endpoints so the
	// control plane can distribute them to peers via HTTPS heartbeat
	// responses, even when our UDP path to the lighthouse is filtered
	// (e.g. iPhone Personal Hotspot blocks UDP to specific Oracle
	// Cloud IPs). Without this, peers can dial us via cached endpoints
	// from A1 but lose us when our CGNAT mapping changes during idle.
	if eps := selfEndpoints(inst); len(eps) > 0 {
		reqBody["selfEndpoints"] = eps
		// Layer 1 (v0.10.27): per-endpoint lifetime hints in seconds, in
		// MATCHING ORDER with selfEndpoints. Server uses these as expiry
		// timestamps so NAT-PMP-mapped entries get pruned automatically
		// when the router reassigns the external port. 0 = no lifetime
		// hint, server applies the default TTL. Old servers without
		// Layer 1 simply ignore this field — backward-compatible.
		if lifetimes := selfEndpointLifetimes(inst, eps); len(lifetimes) > 0 {
			reqBody["selfEndpointLifetimesSec"] = lifetimes
		}
	}
	reqBodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	authToken, err := readInstanceToken(inst)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", inst.endpoint()+"/api/heartbeat", bytes.NewReader(reqBodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)

	// DisableKeepAlives: each call dials a fresh TCP+TLS conn instead of
	// reusing http.DefaultTransport's idle pool. Prevents post-network-
	// change heartbeats (sleep/wake, hotspot toggle, DHCP renew to a
	// different gateway) from picking up a now-defunct idle conn and
	// timing out for minutes until Go's idle timer ages it out.
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		// With multi-enrollment we can no longer os.Exit on a single
		// instance's revocation without taking down the other
		// enrollments. Fail this POST loudly and let the outer retry
		// loop back off. Operators will see "offline" on the
		// dashboard and remove the node; the agent side's full
		// cleanup ships with `hop-agent leave` (Phase D).
		return fmt.Errorf("%w for enrollment %q", errNodeRevoked, inst.name())
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return errRateLimited
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// Warm peer tunnels from heartbeat response. Also ingest peer-relay
	// info (Pillar 3) — `amRelay` flips this node into relay mode,
	// `relays` is the list of OTHER nodes the agent should add to its
	// `relay.relays` set so it can use them as fallback paths.
	//
	// `peerEndpoints` carries each peer's advertised UDP endpoints from
	// the server's lighthouse cache (includes NAT-PMP mappings). Injected
	// into Nebula's hostmap via patch 20's AddStaticHostMap so the agent
	// can handshake directly with peers even when the UDP lighthouse is
	// unreachable (e.g. carrier-filtered cellular to Oracle Cloud).
	var body struct {
		// NetworkID is the server's UUID for this enrollment's network.
		// Phase II.3 (v0.11.3): persisted lazily into the Enrollment
		// registry so the desktop client can build the dashboard's
		// terminal URL (/terminal/{networkId}/{nodeId}). Backwards-
		// compatible: older server builds omit it, the agent leaves
		// the existing value alone.
		NetworkID     string              `json:"networkId"`
		Peers         []string            `json:"peers"`
		Relays        []string            `json:"relays"`
		AmRelay       bool                `json:"amRelay"`
		PeerEndpoints map[string][]string `json:"peerEndpoints"`
		// PeerInfo is human-readable per-peer identifiers (name +
		// DNS hostnames). Optional; older server builds omit it.
		// Stored on inst.peerInfoCache and surfaced via /local/peers.
		PeerInfo map[string]peerInfoEntry `json:"peerInfo"`
		// Routes is the per-node subnet-routing list (roadmap #5).
		// Server-supplied CIDRs the node should advertise as
		// `tun.unsafe_routes` in nebula.yaml. Backwards-compatible:
		// older server builds omit; agent leaves existing routes alone.
		Routes []string `json:"routes,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err == nil {
		if len(body.PeerEndpoints) > 0 {
			injectPeerEndpoints(inst, body.PeerEndpoints)
		}
		if len(body.Peers) > 0 {
			go warmPeers(body.Peers)
		}
		updatePeerInfoCache(inst, body.PeerInfo)
		_ = saveRelayState(inst, body.AmRelay, body.Relays)
		// Apply the routes list. applyRoutesUpdate persists to disk
		// + writes the unsafe_routes block to nebula.yaml + triggers
		// a Nebula reload only when the route list actually changed.
		// No-op when both old and new lists are empty.
		applyRoutesUpdate(inst, body.Routes)
		// Persist networkId on first successful heartbeat (or refresh
		// if the registry value drifted — defensive). Idempotent for
		// stable enrollments; SetNetworkID returns nil with no write
		// if the value already matches.
		if body.NetworkID != "" && inst.enrollment.NetworkID != body.NetworkID {
			inst.enrollment.NetworkID = body.NetworkID
			if reg, err := loadEnrollmentRegistry(configDir); err == nil {
				if err := reg.SetNetworkID(inst.name(), body.NetworkID); err != nil {
					log.Printf("[renew %s] failed to persist networkId: %v", inst.name(), err)
				}
			}
		}
	}
	// Phase P: stamp heartbeat success so enrollmentStatus.Connected
	// has a non-peer signal of "agent is talking to the control plane
	// successfully right now". Used to keep the green pill from going
	// red during the first-startup window where peers haven't done
	// their first handshake yet.
	inst.markHeartbeatSuccess()
	return nil
}

// peerInfoEntry mirrors the server's response shape — see RenewHandler's
// peerInfoEntry in internal/api/renew.go. Defined here so cmd/agent can
// decode without dragging the api package into the agent build.
type peerInfoEntry struct {
	Name           string   `json:"name,omitempty"`
	DnsHostname    string   `json:"dnsHostname,omitempty"`
	CustomDnsNames []string `json:"customDnsNames,omitempty"`
	// OS mirrors the server-side peerInfoEntry.OS (Phase II.2,
	// v0.11.2). Used by the desktop client's peers list to render
	// per-peer OS labels. Backwards-compatible: omitempty so older
	// servers that don't include this field don't break decoding.
	OS string `json:"os,omitempty"`
	// NodeID mirrors the server-side peerInfoEntry.NodeID (Phase II.3,
	// v0.11.3). Used by the desktop client's "Terminal" button to
	// route to the dashboard's per-node terminal page.
	NodeID string `json:"nodeId,omitempty"`
}

// updatePeerInfoCache replaces inst.peerInfoCache with the latest
// server-reported entries. Entries for peers that disappear from the
// server's view are evicted so /local/peers doesn't surface stale
// names long after a peer was removed from the network.
func updatePeerInfoCache(inst *meshInstance, info map[string]peerInfoEntry) {
	if inst == nil {
		return
	}
	// Build a set of fresh keys for eviction.
	fresh := make(map[string]struct{}, len(info))
	for ip, entry := range info {
		fresh[ip] = struct{}{}
		inst.peerInfoCache.Store(ip, entry)
	}
	inst.peerInfoCache.Range(func(k, _ any) bool {
		if ip, ok := k.(string); ok {
			if _, present := fresh[ip]; !present {
				inst.peerInfoCache.Delete(ip)
			}
		}
		return true
	})
}

// readInstanceToken reads the bearer token from the instance's subdir.
// Cached token removal (re-enrollment) is rare; we re-read each time
// rather than caching so credential rotation picks up automatically.
func readInstanceToken(inst *meshInstance) (string, error) {
	data, err := os.ReadFile(filepath.Join(inst.dir(), "token"))
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// injectPeerEndpoints feeds peer advertised UDP endpoints (learned by the
// server's lighthouse, e.g. a peer's NAT-PMP public mapping) directly
// into this instance's Nebula hostmap. This is the agent-side counterpart
// to patch 20's AddStaticHostMap.
//
// Why: when UDP to the lighthouse is carrier-blocked (iPhone hotspot to
// Oracle Cloud, etc.), the agent can't learn peer endpoints via the
// normal Nebula HostQuery flow. The HTTPS control-plane heartbeat is
// still reachable, so the server pushes peer endpoints in-band via the
// `peerEndpoints` response field and we inject them here. MBP can then
// handshake directly to mini's home router public endpoint without
// needing a live lighthouse path.
//
// Best-effort: invalid entries are skipped; no lighthouse means no-op.
func injectPeerEndpoints(inst *meshInstance, peerEndpoints map[string][]string) {
	if inst == nil || len(peerEndpoints) == 0 {
		return
	}
	ctrl := inst.control()
	selfIP := inst.meshIP()
	subnet := inst.meshSubnet()
	if ctrl != nil {
		for ipStr, epStrs := range peerEndpoints {
			vpn, ok := acceptPeerEndpoint(inst.name(), ipStr, selfIP, subnet)
			if !ok {
				continue
			}
			addrs := make([]netip.AddrPort, 0, len(epStrs))
			for _, s := range epStrs {
				ap, err := netip.ParseAddrPort(s)
				if err != nil || !ap.IsValid() {
					continue
				}
				addrs = append(addrs, ap)
			}
			if len(addrs) == 0 {
				continue
			}
			// Layer 2 (v0.10.27): use ReplaceStaticHostMap (patch 21)
			// instead of AddStaticHostMap. The replace variant prunes
			// stale Reported entries from prior heartbeats. Each
			// heartbeat's peerEndpoints is the server's authoritative
			// current set; older addresses (e.g. a previous NAT-PMP
			// allocation that has since rotated to a new external port)
			// must not linger in the hostmap, otherwise the connection
			// manager probes them and traffic gets routed to whoever
			// the upstream router has now reassigned the port to.
			ctrl.ReplaceStaticHostMap(vpn, addrs)
		}
	}
	// Persist the snapshot so the next agent restart can inject the
	// same endpoints BEFORE the first handshake fires (eliminates the
	// relay-vs-direct race that collapses cold-start TCP cwnd).
	if err := savePeerCache(inst, peerEndpoints); err != nil {
		log.Printf("[agent %s] peer cache save failed: %v", inst.name(), err)
	}
}

// acceptPeerEndpoint applies the two cross-defense filters that injectPeerEndpoints
// and injectCachedPeerEndpoints share: drop self-loop entries (Fix E v0.10.26),
// and drop cross-network entries (Layer 3 v0.10.27).
//
// Returns the parsed VPN address and ok=true if the entry should be injected
// into Nebula's hostmap. ok=false means the caller should skip this entry
// silently — this function logs the rejection for operators.
//
// Why a shared helper: both call sites had drift potential — the original
// inline filters duplicated the same logic, and any future fix had to be
// applied in both places. Extracting also makes the filter unit-testable
// without spinning up a Nebula control.
//
// instName is for log context; selfIP and subnet are the enrollment-scoped
// invariants the caller already computed.
func acceptPeerEndpoint(instName, ipStr, selfIP string, subnet netip.Prefix) (netip.Addr, bool) {
	// Fix E (v0.10.26): never inject this enrollment's OWN VPN IP.
	// The server's `p.ID == node.ID` skip in internal/api/renew.go
	// should prevent this, but belt-and-braces against future
	// regressions. Self-injection causes "Refusing to handshake with
	// myself" log noise on every probe.
	if selfIP != "" && ipStr == selfIP {
		log.Printf("[agent %s] skipping self-loop peer endpoint for own VPN IP %s", instName, ipStr)
		return netip.Addr{}, false
	}
	vpn, err := netip.ParseAddr(ipStr)
	if err != nil || !vpn.IsValid() {
		return netip.Addr{}, false
	}
	// Layer 3 (cross-network defense, v0.10.27): drop any peerEndpoint
	// whose VPN address falls outside this enrollment's mesh subnet.
	// Per-network scoping in internal/api/renew.go already filters by
	// network on the server, but a server-side regression OR a stale
	// on-disk peer-cache from a previous re-enrollment OR a NAT-PMP
	// port reuse interaction could land cross-network entries here.
	// Without this guard, Nebula's hostmap accumulates wrong-network
	// entries that connection_manager then probes, generating
	// "Invalid certificate from host" log spam at the receiver and
	// dead-tunnel cycles. If meshSubnet() is invalid (cert unreadable
	// during cold-start race), we fall through to the original
	// behavior — never WORSE than today.
	if subnet.IsValid() && !subnet.Contains(vpn) {
		log.Printf("[agent %s] dropping cross-network peer endpoint vpnAddr=%s (this enrollment's subnet=%s)", instName, ipStr, subnet)
		return netip.Addr{}, false
	}
	return vpn, true
}

func warmPeers(peers []string) {
	for _, ip := range peers {
		d := net.Dialer{Timeout: time.Second}
		if conn, err := d.Dial("tcp", net.JoinHostPort(ip, "41820")); err == nil {
			conn.Close()
		}
	}
}

// renewalPollInterval is how often runCertRenewal wakes to check
// the cert's wall-clock NotAfter. We DELIBERATELY do NOT use
// time.After(longSleep) because Go's runtime timers are anchored to
// monotonic time, which freezes during macOS deep-sleep (Darwin's
// mach_absolute_time stops counting when the CPU TSC freezes). On a
// laptop that closes its lid every night, time.After(5h49m) can take
// days to fire because the awake-time accumulates slowly. Polling
// against the cert's wall-clock NotAfter instead means: after wake,
// the next 60s tick reads cert from disk, sees expiry approaching,
// and renews. Catch-up cost is negligible (~one cert read + parse
// per minute). 60s is small enough that even worst-case sleep events
// don't push renewal past the 50% midpoint by more than a tick.
// Var so tests can shorten it.
var renewalPollInterval = 60 * time.Second

// renewRetryFirstWait is the wait before the first retry of a failed renewal;
// it doubles up to 30 minutes. Var so tests can shorten it.
var renewRetryFirstWait = time.Minute

// minRenewalInterval is the least time between two successful renewals by
// one loop. Only reached when the local clock is far ahead of the server's.
const minRenewalInterval = time.Hour

// runCertRenewal runs a background loop that renews the Nebula certificate
// before it expires. Renews halfway through its life (see renewalTime).
// Exits the process if the node has been deleted (HTTP 401).
//
// Wall-clock polling architecture (Phase S, post-2026-05-02 incident):
// instead of time.After(longSleep) the loop polls every 60s and
// reads the cert's NotAfter directly. This is laptop-sleep-safe —
// monotonic clocks freeze during deep sleep but cert.NotAfter() is
// wall-clock-anchored, so post-wake the next tick correctly
// identifies "renewal overdue" and fires.
//
// One goroutine per meshInstance; each watches its own cert.
func runCertRenewal(ctx context.Context, inst *meshInstance) {
	// Phase P: stamp activity at every observable point so the
	// renewal watchdog (cmd/agent/renew.go::runRenewalWatchdog)
	// can detect a silent goroutine death.
	inst.markRenewalActivity()
	log.Printf("[renew %s] loop entered (wall-clock polling, %s tick)", inst.name(), renewalPollInterval)

	ticker := time.NewTicker(renewalPollInterval)
	defer ticker.Stop()

	// lastRenewed is the wall-clock time of the last successful renewal in
	// this loop. If the cert still looks due right after one, the local
	// clock is far ahead of the server's; renewing on every poll would
	// restart the tunnel every minute and fix nothing.
	var lastRenewed time.Time
	renewedRecently := func() bool {
		since := time.Now().Round(0).Sub(lastRenewed)
		return !lastRenewed.IsZero() && since >= 0 && since < minRenewalInterval
	}

	// renewNow performs the renewal POST + retry-with-backoff
	// sequence. Returns nil on success, or last error after exhausting
	// 12 retries. Inline so the timer logic stays compact.
	renewNow := func() error {
		log.Printf("[renew %s] cert past renewal threshold, attempting renewal POST", inst.name())
		inst.markRenewalActivity()
		if err := renewCert(inst); err == nil {
			inst.markRenewalActivity()
			return nil
		} else {
			log.Printf("[renew %s] renewal failed: %v (entering retry-with-backoff)", inst.name(), err)
			inst.markRenewalActivity()
			backoff := renewRetryFirstWait
			for attempt := 0; attempt < 12; attempt++ {
				if !waitWallClock(ctx, backoff) {
					log.Printf("[renew %s] retry loop exiting (ctx cancelled)", inst.name())
					return ctx.Err()
				}
				inst.markRenewalActivity()
				log.Printf("[renew %s] retry %d/12 attempting", inst.name(), attempt+1)
				if err := renewCert(inst); err != nil {
					log.Printf("[renew %s] retry %d failed: %v", inst.name(), attempt+1, err)
					backoff *= 2
					if backoff > 30*time.Minute {
						backoff = 30 * time.Minute
					}
					// Without a valid certificate the network is down until this works, so
					// the wait does not grow. A 401 is usually final and keeps the long wait.
					// A minute is also what a 429 asks for (rateLimitedMinRetry).
					if problem, _ := certStartProblem(inst.dir(), time.Now()); problem != "" && !errors.Is(err, errNodeRevoked) {
						backoff = renewRetryFirstWait
					}
					continue
				}
				inst.markRenewalActivity()
				return nil
			}
			return fmt.Errorf("renewal exhausted 12 retries")
		}
	}

	// Initial check: at startup, immediately evaluate whether the
	// cert needs renewal. Covers the cold-start-with-expired-cert
	// case (e.g. laptop slept past expiry, agent just respawned).
	for {
		renewAt, err := timeUntilRenewal(inst)
		inst.markRenewalActivity()
		if err != nil {
			log.Printf("[renew %s] could not determine renewal time: %v (will retry on next poll)", inst.name(), err)
		} else if renewAt <= 0 && renewedRecently() {
			log.Printf("[renew %s] cert still looks due after a renewal at %s — is the system clock ahead? Not renewing again yet",
				inst.name(), lastRenewed.Format(time.RFC3339))
		} else if renewAt <= 0 {
			log.Printf("[renew %s] cert needs renewal NOW (past half its life)", inst.name())
			if renewNow() == nil {
				lastRenewed = time.Now().Round(0)
			}
		} else {
			log.Printf("[renew %s] cert OK, renewal due in %s (next poll in %s)",
				inst.name(), renewAt.Truncate(time.Second), renewalPollInterval)
			_ = err // suppress unused
			_ = renewAt
		}

		select {
		case <-ctx.Done():
			log.Printf("[renew %s] loop exiting (ctx cancelled)", inst.name())
			return
		case <-ticker.C:
			inst.markRenewalActivity()
		}
	}
}

// timeUntilRenewal reads the current cert and returns how long until it
// should be renewed; <= 0 means now.
func timeUntilRenewal(inst *meshInstance) (time.Duration, error) {
	certPEM, err := os.ReadFile(filepath.Join(inst.dir(), "node.crt"))
	if err != nil {
		return 0, fmt.Errorf("read cert: %w", err)
	}

	c, _, err := cert.UnmarshalCertificateFromPEM(certPEM)
	if err != nil {
		return 0, fmt.Errorf("parse cert: %w", err)
	}
	return time.Until(renewalTime(c.NotBefore(), c.NotAfter(), inst.nodeID())), nil
}

// renewalTime is when a cert should be renewed: halfway through its life,
// ±10% of its life so agents that enrolled together don't renew together.
//
// It MUST be a fixed instant for a given cert. From v0.10.82 to v0.11.26 the
// deadline was "half of the time remaining", re-evaluated on every poll —
// which is always in the future while any time remains, so no agent ever
// renewed before its cert had already expired (Zeno's renewal). A machine
// that was off or offline at that moment came back with an expired cert.
func renewalTime(notBefore, notAfter time.Time, seed string) time.Time {
	life := notAfter.Sub(notBefore)
	h := fnv.New32a()
	fmt.Fprintf(h, "%s/%d", seed, notAfter.Unix())
	// frac is in [-1, 1).
	frac := float64(h.Sum32())/float64(1<<31) - 1
	return notBefore.Add(life/2 + time.Duration(frac*float64(life/10)))
}

// writeCertPair puts a renewed cert and key on disk as one pair.
//
// certInstallMu keeps two overlapping renewals from leaving one's cert next to
// the other's key. startMu is the other half of that, and the half that was
// missing: connect holds it while Nebula opens node.crt and node.key one after
// the other, and the renewal loop is already running by then. A renewal that
// lands in that window leaves Nebula with the new cert and the old key, and it
// refuses to start at all — "private key is not a pair with public key in
// nebula cert" — so the network comes up on the OS stack and Nebula only
// arrives on the retry two seconds later. The window is open on every start of
// a machine that was off for more than half a cert's life, which is 12 h of
// the 24 h we issue; neither Mac in the fleet has logged it, so it is a race
// that is common to hit, not one that has been seen.
//
// A crash between the two writes is still caught by certStartProblem
// ("mismatched") on the next start; this closes the window where nothing
// crashed and a read simply happened in the middle.
func (inst *meshInstance) writeCertPair(certPEM, keyPEM string) error {
	inst.startMu.Lock()
	defer inst.startMu.Unlock()

	certPath := filepath.Join(inst.dir(), "node.crt")
	if err := atomicWrite(certPath, []byte(certPEM), 0644); err != nil {
		return fmt.Errorf("write cert: %w", err)
	}
	keyPath := filepath.Join(inst.dir(), "node.key")
	if err := atomicWrite(keyPath, []byte(keyPEM), 0600); err != nil {
		return fmt.Errorf("write key: %w", err)
	}
	return nil
}

// renewCert calls the control plane's /api/renew endpoint to get a fresh
// cert, installs it, and hot-reloads Nebula onto it.
func renewCert(inst *meshInstance) error {
	if err := fetchAndInstallCert(inst); err != nil {
		return err
	}

	// Signal Nebula to reload certs (and pick up any config changes).
	reloadNebula(inst)

	log.Printf("[renew %s] certificate renewed successfully", inst.name())
	return nil
}

// certInstallMu serializes writing a renewed node.crt + node.key pair.
var certInstallMu sync.Mutex

// fetchAndInstallCert POSTs /api/renew and atomically writes the returned
// node.crt + node.key (plus any server-pushed Nebula config) into the
// enrollment dir. It does NOT touch the running Nebula instance, so it is
// safe to call before Nebula has ever started — the pre-start renewal in
// Client.connect relies on that (recovery.go). Authenticated by the
// agent bearer token, never by the cert, so an expired cert can always be
// replaced.
func fetchAndInstallCert(inst *meshInstance) error {
	reqBody := fmt.Sprintf(`{"nodeId":%q}`, inst.nodeID())
	req, err := http.NewRequestWithContext(inst.lifetimeCtx(), "POST", inst.endpoint()+"/api/renew", strings.NewReader(reqBody))
	if err != nil {
		return err
	}
	authToken, err := readInstanceToken(inst)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)

	// DisableKeepAlives: see matching comment in sendHeartbeat.
	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		// Multi-enrollment: see the matching note in sendHeartbeat.
		// One deleted node shouldn't take down the whole agent.
		return fmt.Errorf("%w for enrollment %q", errNodeRevoked, inst.name())
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return errRateLimited
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}

	var renewResp struct {
		NodeCert     string              `json:"nodeCert"`
		NodeKey      string              `json:"nodeKey"`
		NebulaConfig *nebulaConfigUpdate `json:"nebulaConfig,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&renewResp); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	if renewResp.NodeCert == "" || renewResp.NodeKey == "" {
		return fmt.Errorf("empty cert or key in response")
	}

	certInstallMu.Lock()
	defer certInstallMu.Unlock()

	if err := inst.writeCertPair(renewResp.NodeCert, renewResp.NodeKey); err != nil {
		return err
	}

	// Apply server-pushed Nebula config if present.
	if renewResp.NebulaConfig != nil {
		if err := applyNebulaConfigUpdate(inst, renewResp.NebulaConfig); err != nil {
			log.Printf("[renew %s] failed to apply config update: %v (continuing with old config)", inst.name(), err)
		}
	}
	return nil
}

// nebulaConfigUpdate contains server-pushed Nebula settings.
// Pointer fields: nil means "don't change", non-nil means "set to this value".
type nebulaConfigUpdate struct {
	UseRelays  *bool  `json:"useRelays,omitempty"`
	PunchBack  *bool  `json:"punchBack,omitempty"`
	PunchDelay string `json:"punchDelay,omitempty"`
	MTU        *int   `json:"mtu,omitempty"`
	ListenPort *int   `json:"listenPort,omitempty"`
}

// applyNebulaConfigUpdate merges server-pushed settings into the local nebula.yaml.
func applyNebulaConfigUpdate(inst *meshInstance, update *nebulaConfigUpdate) error {
	// Fix B (v0.10.26): defensive reject of mismatched server-pushed
	// listen port. The server (post-v0.10.26) no longer pushes
	// listenPort, but if a regression or older server pushes one that
	// doesn't match this enrollment's local allocation, IGNORE it.
	// The enrollment registry (`enrollments.json`) is the source of
	// truth for per-enrollment listen ports — only the agent knows
	// which ports its other enrollments are using on the same host.
	//
	// History: pre-v0.10.26 the server hardcoded listenPort=4242 and
	// pushed it on every renewal, silently corrupting multi-enrollment
	// hosts whose secondary enrollment was on 4243+. The corruption
	// caused a port-bind collision in reloadNebula, which left the
	// enrollment without a running Nebula AND without a network-change
	// watcher, breaking sleep recovery as a corollary.
	if update != nil && update.ListenPort != nil {
		if inst != nil && inst.enrollment != nil && inst.enrollment.ListenPort > 0 &&
			*update.ListenPort != inst.enrollment.ListenPort {
			log.Printf("[renew %s] ignoring server-pushed listenPort=%d, keeping enrollment's allocated port %d",
				inst.name(), *update.ListenPort, inst.enrollment.ListenPort)
			update.ListenPort = nil
		}
	}

	configPath := filepath.Join(inst.dir(), "nebula.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	out, changed, err := mergeNebulaConfig(data, update)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}

	if err := atomicWrite(configPath, out, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	log.Printf("[renew] nebula config updated from server")
	return nil
}

// mergeNebulaConfig applies a config update to raw YAML bytes and returns the
// result. Pure function — no side effects, easy to test. Returns (output, changed, error).
func mergeNebulaConfig(data []byte, update *nebulaConfigUpdate) ([]byte, bool, error) {
	cfg, err := parseNebulaYAML(data)
	if err != nil {
		return nil, false, fmt.Errorf("parse config: %w", err)
	}

	changed := false

	if update.UseRelays != nil {
		relay := yamlMap(cfg, "relay")
		relay["use_relays"] = *update.UseRelays
		cfg["relay"] = relay
		changed = true
	}

	if update.PunchBack != nil || update.PunchDelay != "" {
		punchy := yamlMap(cfg, "punchy")
		if update.PunchBack != nil {
			punchy["punch_back"] = *update.PunchBack
			changed = true
		}
		if update.PunchDelay != "" {
			punchy["delay"] = update.PunchDelay
			changed = true
		}
		cfg["punchy"] = punchy
	}

	// Update listen.port — fixed port is critical for NAT hole punching.
	if update.ListenPort != nil {
		listen := yamlMap(cfg, "listen")
		listen["port"] = *update.ListenPort
		cfg["listen"] = listen
		changed = true
	}

	// Server-pushed MTU is intentionally IGNORED. The server stopped
	// pushing it as of v0.10.17 (see internal/api/renew.go for why), but
	// older servers in the wild may still send it. Honoring it would let
	// an older server clobber a newer agent's correct local MTU during
	// the renewal window — which is exactly the bug we're guarding
	// against. The agent's local `nebulacfg.TunMTU` (written by
	// `ensureP2PConfig` at startup) is the single source of truth for
	// MTU. If a per-network admin override is added later, it'll need a
	// distinct field name so we can honor it without re-introducing
	// this regression.
	_ = update.MTU

	if !changed {
		return data, false, nil
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, false, fmt.Errorf("marshal config: %w", err)
	}
	return out, true, nil
}

// parseNebulaYAML parses a nebula.yaml. An empty file (a write cut short by a
// crash) is an error: yaml.Unmarshal accepts it and leaves a nil map, which
// panics on the first write and took the whole agent down on every boot.
func parseNebulaYAML(data []byte) (map[string]interface{}, error) {
	var cfg map[string]interface{}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, errors.New("file is empty")
	}
	return cfg, nil
}

// yamlMap returns a nested map from a config, creating it if absent.
func yamlMap(cfg map[string]interface{}, key string) map[string]interface{} {
	if m, ok := cfg[key].(map[string]interface{}); ok {
		return m
	}
	m := make(map[string]interface{})
	cfg[key] = m
	return m
}

// ensureP2PConfig updates nebula.yaml with settings critical for P2P:
// - Fixed listen port (NAT mapping stability)
// - target_all_remotes (continuous relay→direct upgrade)
// - local_allow_list with physical interface (prevents overlay-within-overlay)
// - Fast punch timing
// - PKI paths match inst.dir() (fixes legacy flat-layout migrations
//   where the yaml still references the pre-migration paths)
func ensureP2PConfig(inst *meshInstance) {
	configPath := filepath.Join(inst.dir(), "nebula.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return
	}

	cfg, err := parseNebulaYAML(data)
	if err != nil {
		log.Printf("[agent %s] WARNING: nebula.yaml: %v", inst.name(), err)
		return
	}

	changed := false

	// PKI paths: rewrite if they don't match the instance's subdir.
	// Covers both fresh enrollments (already correct) and post-
	// migration state where the yaml was moved but its pki block
	// still points at the flat-layout paths.
	pki := yamlMap(cfg, "pki")
	wantCA := filepath.Join(inst.dir(), "ca.crt")
	wantCert := filepath.Join(inst.dir(), "node.crt")
	wantKey := filepath.Join(inst.dir(), "node.key")
	if pki["ca"] != wantCA {
		pki["ca"] = wantCA
		changed = true
	}
	if pki["cert"] != wantCert {
		pki["cert"] = wantCert
		changed = true
	}
	if pki["key"] != wantKey {
		pki["key"] = wantKey
		changed = true
	}
	if changed {
		cfg["pki"] = pki
	}

	// Listen port is per-enrollment (assigned at enroll time, persisted
	// in Enrollment.ListenPort). Self-heal nebula.yaml if the on-disk
	// listen.port has drifted (legacy enrollments used port 0 = random;
	// the migrate step assigned a stable port and updated yaml, but a
	// later config write or hand-edit could re-introduce drift).
	listen := yamlMap(cfg, "listen")
	if inst.enrollment != nil && inst.enrollment.ListenPort > 0 {
		curPort, _ := listen["port"].(int)
		if curPort != inst.enrollment.ListenPort {
			listen["port"] = inst.enrollment.ListenPort
			changed = true
		}
	}
	if _, ok := listen["read_buffer"]; ok {
		delete(listen, "read_buffer")
		changed = true
	}
	if _, ok := listen["write_buffer"]; ok {
		delete(listen, "write_buffer")
		changed = true
	}
	cfg["listen"] = listen

	// Ensure cipher matches default (AES-GCM, hardware-accelerated on Apple Silicon).
	if c, ok := cfg["cipher"]; !ok || c != nebulacfg.Cipher {
		cfg["cipher"] = nebulacfg.Cipher
		changed = true
	}

	// Connection-manager tolerance (Option C, v0.10.25). Nebula's
	// default 5s probe + 10s deletion-grace was dead-marking active
	// tunnels every ~15min during screen-share because multi-path
	// kernel routing caused individual probe packets to be lost. The
	// looser timers (10s + 30s = 40s total) absorb transient packet
	// loss without affecting genuine network-change recovery (which
	// is handled separately by watchNetworkChanges + forced rebind).
	timers := yamlMap(cfg, "timers")
	if v, ok := timers["connection_alive_interval"]; !ok || v != nebulacfg.ConnectionAliveIntervalSec {
		timers["connection_alive_interval"] = nebulacfg.ConnectionAliveIntervalSec
		changed = true
	}
	if v, ok := timers["pending_deletion_interval"]; !ok || v != nebulacfg.PendingDeletionIntervalSec {
		timers["pending_deletion_interval"] = nebulacfg.PendingDeletionIntervalSec
		changed = true
	}
	cfg["timers"] = timers

	// Punchy settings for fast P2P establishment.
	punchy := yamlMap(cfg, "punchy")
	if tar, ok := punchy["target_all_remotes"]; !ok || tar != true {
		punchy["target_all_remotes"] = true
		changed = true
	}
	if punchy["delay"] != nebulacfg.PunchDelay {
		punchy["delay"] = nebulacfg.PunchDelay
		changed = true
	}
	if punchy["respond_delay"] != nebulacfg.RespondDelay {
		punchy["respond_delay"] = nebulacfg.RespondDelay
		changed = true
	}
	if changed {
		cfg["punchy"] = punchy
	}

	// Faster handshake retry for quick tunnel establishment.
	handshakes := yamlMap(cfg, "handshakes")
	if ti, ok := handshakes["try_interval"]; !ok || ti != nebulacfg.HandshakeTryInterval {
		handshakes["try_interval"] = nebulacfg.HandshakeTryInterval
		cfg["handshakes"] = handshakes
		changed = true
	}

	// Normalize kernel-TUN dev name to hop-<enrollment>. Required on
	// Linux to avoid collisions when two instances run in the same
	// process (kernel rejects duplicate IFNAME). macOS ignores the
	// field (utun auto-assigned) but we still keep it consistent for
	// log readability.
	if tun, ok := cfg["tun"].(map[string]interface{}); ok {
		if mtu, hasMTU := tun["mtu"]; hasMTU {
			if mtuInt, ok := mtu.(int); ok && mtuInt != nebulacfg.TunMTU {
				tun["mtu"] = nebulacfg.TunMTU
				cfg["tun"] = tun
				changed = true
			}
		}
		// Only rewrite dev when the tun block actually has a dev key
		// (kernel mode) — userspace installs use `user: true` and no
		// dev field, which stays untouched.
		if _, hasDev := tun["dev"]; hasDev {
			want := meshIfaceName(inst.name())
			if tun["dev"] != want {
				tun["dev"] = want
				cfg["tun"] = tun
				changed = true
			}
		}
	}

	// macOS sends broadcasts into the mesh after every network change. Nebula
	// delivers them to nobody and tries a handshake with x.x.x.255.
	if tun := yamlMap(cfg, "tun"); tun["drop_local_broadcast"] != true {
		tun["drop_local_broadcast"] = true
		cfg["tun"] = tun
		changed = true
	}

	// Parallel packet processing routines (effective on Linux with multiqueue TUN).
	if r, ok := cfg["routines"]; !ok || r != nebulacfg.Routines {
		cfg["routines"] = nebulacfg.Routines
		changed = true
	}

	// Up to v0.11.27 the ranges were under "lighthouse", where Nebula doesn't look.
	lighthouse := yamlMap(cfg, "lighthouse")
	ranges, misplaced := lighthouse["preferred_ranges"]
	if misplaced {
		delete(lighthouse, "preferred_ranges")
		changed = true
	}
	if _, ok := cfg["preferred_ranges"]; !ok {
		if ranges == nil {
			ranges = nebulacfg.PreferredRanges
		}
		cfg["preferred_ranges"] = ranges
		changed = true
	}

	// Detect physical interface and set local_allow_list.
	// This prevents Nebula from advertising overlay IPs (ZeroTier, etc.)
	// while still allowing the lighthouse to learn our public IP from
	// the UDP source address.
	host := extractHost(inst.endpoint())
	if host != "" {
		if iface, err := nebulacfg.DetectPhysicalInterface(host); err == nil {
			lighthouse := yamlMap(cfg, "lighthouse")
			escaped := regexp.QuoteMeta(iface)
			lighthouse["local_allow_list"] = map[string]interface{}{
				"interfaces": map[string]interface{}{
					escaped: true,
				},
			}
			cfg["lighthouse"] = lighthouse
			changed = true
			log.Printf("[agent] local_allow_list set to interface %s", iface)
		} else {
			log.Printf("[agent] could not detect physical interface: %v", err)
		}
	}

	// Apply cached peer-relay state (Pillar 3): if the dashboard has
	// flagged this node as a relay, write `relay.am_relay: true`; if
	// other relay-capable peers exist, extend `relay.relays` with them.
	// `loadRelayState` returns nil if no cache exists yet (default
	// behavior — no relay role, only the lighthouse as relay).
	if state, _ := loadRelayState(inst); state != nil {
		relay := yamlMap(cfg, "relay")

		curAmRelay, _ := relay["am_relay"].(bool)
		if curAmRelay != state.AmRelay {
			relay["am_relay"] = state.AmRelay
			changed = true
		}

		// Merge cached peer-relay IPs into relay.relays without
		// dropping the lighthouse(s) the enrollment originally listed.
		if len(state.Relays) > 0 {
			merged := mergeRelayList(relay["relays"], state.Relays)
			if !relayListEqual(relay["relays"], merged) {
				relay["relays"] = merged
				changed = true
			}
		}

		cfg["relay"] = relay
	}

	if !changed {
		return
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return
	}

	if err := atomicWrite(configPath, out, 0644); err != nil {
		log.Printf("[agent] WARNING: failed to update P2P config: %v", err)
		return
	}
	log.Printf("[agent] P2P config updated (port: %d, target_all_remotes: true)", nebulacfg.ListenPort)
}

// mergeRelayList combines the existing `relay.relays` list (whatever
// shape yaml.Unmarshal produced) with peer-relay IPs from cached
// state. Output is a sorted, deduped []interface{} compatible with
// yaml.Marshal — peer IPs are added IF NOT already present, original
// entries (the lighthouse) are preserved.
func mergeRelayList(existing any, peerRelays []string) []any {
	seen := map[string]bool{}
	out := []any{}
	switch list := existing.(type) {
	case []any:
		for _, item := range list {
			if s, ok := item.(string); ok && s != "" && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	case []string:
		for _, s := range list {
			if s != "" && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	for _, ip := range peerRelays {
		if ip == "" || seen[ip] {
			continue
		}
		seen[ip] = true
		out = append(out, ip)
	}
	return out
}

// relayListEqual compares two yaml-shaped relay lists for equality,
// treating string and any-string interchangeably.
func relayListEqual(a, b any) bool {
	as := relayListAsStrings(a)
	bs := relayListAsStrings(b)
	if len(as) != len(bs) {
		return false
	}
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

func relayListAsStrings(v any) []string {
	switch list := v.(type) {
	case []any:
		out := make([]string, 0, len(list))
		for _, x := range list {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return list
	}
	return nil
}

// addJitter applies ±10% random jitter to a duration to spread agent load.
func addJitter(d time.Duration) time.Duration {
	jitter := time.Duration(float64(d) * 0.1 * (2*rand.Float64() - 1))
	return d + jitter
}

// atomicWrite writes data to a temp file then renames it to the target path.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// reloadNebula restarts the embedded Nebula instance to pick up new certs.
// Supports both userspace and kernel TUN modes. Scoped to one instance.
//
// Never leaves Nebula running on a closed instance: a renewal that was in
// flight when the user disconnected (or a watchdog restarted) lands here
// after close(), and starting Nebula then would orphan it on the UDP port.
func reloadNebula(inst *meshInstance) {
	inst.startMu.Lock()
	defer inst.startMu.Unlock()
	inst.svcMu.Lock()
	if inst.closed {
		inst.svcMu.Unlock()
		log.Printf("[renew %s] instance was closed — not starting Nebula", inst.name())
		return
	}

	configPath := filepath.Join(inst.dir(), "nebula.yaml")
	tunMode := readTunMode(inst)

	if inst.svc == nil {
		// Nebula never started — e.g. cert was expired at boot. Try a fresh
		// start now that we have a renewed cert. This is the fix for the
		// "screen sharing broken after overnight sleep" bug: agent boots with
		// expired cert → Nebula fails → falls back to OS stack → renewal
		// gets fresh cert → this path starts Nebula for the first time.
		inst.svcMu.Unlock()
		log.Printf("[renew %s] no embedded Nebula instance — attempting cold start with renewed cert", inst.name())

		newSvc, err := startMeshFn(configPath, tunMode)
		if err != nil {
			log.Printf("[renew %s] Nebula cold start failed even after cert renewal: %v", inst.name(), err)
			// The same safety net the hot path has. Without it the next
			// chance is the next renewal, about 13 h later on the 24 h
			// certs, for a failure that is usually the UDP port still
			// held by the start that failed at boot.
			scheduleRetryReload(inst, configPath, tunMode)
			return
		}

		if !inst.setSvc(newSvc) {
			newSvc.Close()
			log.Printf("[renew %s] instance was closed while Nebula was starting — stopped it", inst.name())
			return
		}

		log.Printf("[renew %s] Nebula started after cert renewal (mode: %s)", inst.name(), tunMode)

		// Configure DNS (kernel TUN mode only), beside the rest as in connect.
		if tunMode == "kernel" {
			go inst.configureSplitDNS()
		}

		// Warm tunnels so Screen Sharing HP mode works immediately.
		warmTunnel(configPath)
		if endpoint := inst.endpoint(); endpoint != "" {
			warmPeersFromHeartbeat(inst, endpoint)
		}

		// (Re)start network-change watcher bound to the fresh ctrl.
		if ctrl := newSvc.NebulaControl(); ctrl != nil && inst.endpoint() != "" {
			inst.startWatcher(ctrl)
		}

		// Re-inject any live portmap mapping into the fresh lighthouse's
		// advertise_addrs (cold-start path: portmap wasn't running yet
		// so this is effectively a no-op, but kept for symmetry).
		inst.reinjectPortmapAddr()

		// Swap HTTP listener from OS stack to mesh.
		if inst.onRestart != nil && !inst.isClosed() {
			inst.onRestart(newSvc)
		}
		return
	}

	// Hot-restart path (Fix v0.10.33): close-old-then-start-new with a
	// port-release wait. v0.10.26's try-then-swap order ALWAYS produced
	// "address already in use" because Nebula's listen socket can't
	// share-port; the new bind would fail, leaving the agent stuck on
	// the OLD svc holding an OLD cert. Once that OLD cert expired, peers
	// rejected every handshake (verified in production 2026-04-27 morning).
	//
	// The new order: stop watcher, nil out inst.svc, close old svc to
	// release the UDP port, wait briefly for the kernel to free the port,
	// then start the new svc. The retry-with-backoff (scheduleRetryReload)
	// remains the safety net for any genuinely transient failure.
	//
	// Setting inst.svc = nil BEFORE scheduling retry is load-bearing: it
	// makes scheduleRetryReload's "haveSvc" check correctly identify the
	// "another path restored the svc" case (which is what we want to
	// skip) vs. the "we just failed and stale OLD svc lingers" case
	// (which is exactly what was masking the bug pre-v0.10.33).
	inst.stopWatcher()
	oldSvc := inst.svc
	inst.svc = nil
	inst.svcMu.Unlock()
	if oldSvc != nil {
		oldSvc.Close() // releases UDP listener + (kernel mode) utun
	}

	// Wait for the kernel to release the UDP listen port so the new
	// nebula.Main()'s synchronous bind succeeds. Best-effort: on timeout
	// we proceed and let the bind error surface naturally (and trip the
	// retry-backoff path).
	listenPort := inst.enrollment.ListenPort
	if listenPort == 0 {
		listenPort = nebulacfg.ListenPort
	}
	waitForUDPPortFreeFn(listenPort, 2*time.Second)

	newSvc, err := startNebulaByModeFn(configPath, tunMode)
	if err != nil {
		log.Printf("[renew %s] failed to start new Nebula instance after cert renewal: %v (will retry with backoff)", inst.name(), err)
		// Old svc is gone, inst.svc is nil — retry path can cleanly try again.
		scheduleRetryReload(inst, configPath, tunMode)
		return
	}

	if !inst.setSvc(newSvc) {
		newSvc.Close()
		log.Printf("[renew %s] instance was closed while Nebula was restarting — stopped it", inst.name())
		return
	}

	// Spawn a fresh watcher against the new ctrl.
	if ctrl := newSvc.NebulaControl(); ctrl != nil && inst.endpoint() != "" {
		inst.startWatcher(ctrl)
	}

	// Re-inject any live portmap mapping into the new lighthouse's
	// advertise_addrs (the fresh Control starts with only config-file
	// addrs; without this, peers stop seeing our public endpoint until
	// the mapping next refreshes, which can be up to an hour).
	inst.reinjectPortmapAddr()

	log.Printf("[renew %s] Nebula restarted with new certificate (mode: %s)", inst.name(), tunMode)

	// Notify the HTTP server to recreate its mesh listener.
	if inst.onRestart != nil && !inst.isClosed() {
		inst.onRestart(newSvc)
	}
}

// startNebulaByMode dispatches to kernel-TUN or userspace start based
// on tunMode, and nothing else: it does not fall back to userspace when
// a kernel start fails. The reload paths (hot swap and retry-with-backoff)
// use it, so a reload that cannot get the TUN device fails and is retried
// in the same mode. connect and reloadNebula's cold start use startMeshFn,
// which does fall back.
//
// Indirected via startNebulaByModeFn so tests can substitute a fake
// without standing up a real Nebula process.
var startNebulaByModeFn = startNebulaByMode

func startNebulaByMode(configPath, tunMode string) (meshService, error) {
	if tunMode == "kernel" {
		return startNebulaKernelTun(configPath)
	}
	return startNebula(configPath)
}

// waitForUDPPortFreeFn is the indirection used by reloadNebula's port
// availability wait. Tests substitute this to simulate stuck-port
// scenarios without binding real OS sockets.
var waitForUDPPortFreeFn = waitForUDPPortFree

// waitForUDPPortFree polls until a UDP socket can bind to :port or the
// deadline passes. Used between closing the old Nebula svc and starting
// the new one so the new nebula.Main() bind succeeds reliably.
//
// Timeout is best-effort: on miss we return silently and let the
// subsequent bind attempt surface its own error to the caller, which
// triggers the retry-with-backoff path.
//
// "udp4": see udpPortAvailable.
func waitForUDPPortFree(port int, deadline time.Duration) {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: port})
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitForTUNDeviceFreeFn is the indirection used by the live
// connect-after-disconnect path to wait for a kernel TUN device to be
// released. Tests substitute this. Linux-only; macOS uses auto-assigned
// utun names (no collision possible), Windows uses WinTun (different
// driver model).
var waitForTUNDeviceFreeFn = waitForTUNDeviceFree

// waitForTUNDeviceFree polls until /sys/class/net/<dev> goes away or
// the deadline passes. Used in connectFn to handle the Linux race
// where the kernel takes a moment to release the netdev after
// nebula.Control.Stop() returns. On non-Linux platforms the path
// doesn't exist so the first probe returns immediately (no-op).
//
// Best-effort: on timeout we return silently. The subsequent
// startMeshInstance bind attempt will surface "device or resource
// busy" if the device is genuinely stuck, which the local API
// surfaces to the caller as a 500. The user can retry.
func waitForTUNDeviceFree(devName string, deadline time.Duration) {
	if devName == "" {
		return
	}
	path := "/sys/class/net/" + devName
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if _, err := os.Stat(path); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// retryReloadBackoff is the exponential backoff schedule used by
// scheduleRetryReload after a failed Nebula start during cert renewal.
// Capped at 30 minutes; max 10 attempts. Final give-up logs at ERROR.
//
// Tuning rationale: 5s catches transient binds (port held briefly by
// stale kernel state); 30s/2m absorb most user-side blips; 10m/30m
// cover longer-lived issues (e.g., wrong port collision waiting for
// the OTHER enrollment's renewal to free the port). Total worst-case
// retry window is ~3.5h — well under the 12h cert lifetime so we
// recover before the existing cert expires in the common case.
//
// Stored in an atomic.Pointer so unit tests can substitute a shorter
// schedule (via setRetryReloadBackoffForTest) with proper happens-
// before edges between the test goroutine's writes and the retry
// goroutine's reads. Reads in production code go through
// retryReloadBackoffSchedule().
var retryReloadBackoff atomic.Pointer[[]time.Duration]

func init() {
	defaultSchedule := []time.Duration{
		5 * time.Second,
		30 * time.Second,
		2 * time.Minute,
		5 * time.Minute,
		10 * time.Minute,
		15 * time.Minute,
		30 * time.Minute,
		30 * time.Minute,
		30 * time.Minute,
		30 * time.Minute,
	}
	retryReloadBackoff.Store(&defaultSchedule)
}

// retryReloadBackoffSchedule returns the current schedule. Production
// callers always get the package default; tests may swap via the
// _test.go helper.
func retryReloadBackoffSchedule() []time.Duration {
	if p := retryReloadBackoff.Load(); p != nil {
		return *p
	}
	return nil
}

// scheduleRetryReload spawns a detached goroutine that retries the
// reload-Nebula path with exponential backoff after a failed start. Ends
// with the instance: closing it (disconnect, watchdog restart, agent
// shutdown) cancels any in-progress retry.
//
// Idempotency: if a fresh reload succeeds OR the cert is renewed
// again, the in-flight retry's next attempt will simply find
// inst.svc != nil + valid and exit early.
func scheduleRetryReload(inst *meshInstance, configPath, tunMode string) {
	if inst == nil {
		return
	}
	ctx := inst.lifetimeCtx()

	go func() {
		schedule := retryReloadBackoffSchedule()
		for attempt, backoff := range schedule {
			select {
			case <-ctx.Done():
				log.Printf("[renew %s] retry-reload cancelled (instance closed or agent shutting down)", inst.name())
				return
			case <-time.After(addJitter(backoff)):
			}
			if retryReloadOnce(inst, configPath, tunMode, attempt, len(schedule), backoff) {
				return
			}
		}

		log.Printf("[renew %s] CRITICAL: retry-reload exhausted %d attempts; agent will lose mesh connectivity when current cert expires",
			inst.name(), len(schedule))
	}()
}

// retryReloadOnce makes one scheduleRetryReload attempt. Returns true when
// there is nothing left to retry.
func retryReloadOnce(inst *meshInstance, configPath, tunMode string, attempt, total int, backoff time.Duration) bool {
	inst.startMu.Lock()
	defer inst.startMu.Unlock()

	// Skip if reload already succeeded out-of-band: a manual
	// `launchctl bootstrap`, the next cert-renewal cycle, or
	// any other path has already installed a fresh svc.
	//
	// Precondition (load-bearing): the caller of
	// scheduleRetryReload — i.e. reloadNebula's hot-restart
	// path — MUST set inst.svc = nil BEFORE scheduling. Pre-
	// v0.10.33 it didn't, so this check fired as a false
	// positive on the very state it was supposed to recover
	// from (stale old svc lingering after a failed bind),
	// silently masking the cert-reload-port-bind bug. The
	// fix in reloadNebula (set inst.svc = nil pre-schedule)
	// makes this check correctly distinguish "real restore
	// happened" from "we just failed".
	inst.svcMu.Lock()
	haveSvc, closed := inst.svc != nil, inst.closed
	inst.svcMu.Unlock()
	if closed {
		log.Printf("[renew %s] retry-reload cancelled (instance closed)", inst.name())
		return true
	}
	if haveSvc {
		log.Printf("[renew %s] retry-reload skipping attempt %d/%d: svc already restored",
			inst.name(), attempt+1, total)
		return true
	}

	log.Printf("[renew %s] retry-reload attempt %d/%d after %v backoff",
		inst.name(), attempt+1, total, backoff.Round(time.Second))

	newSvc, err := startNebulaByModeFn(configPath, tunMode)
	if err != nil {
		log.Printf("[renew %s] retry-reload attempt %d/%d failed: %v",
			inst.name(), attempt+1, total, err)
		return false
	}

	// Success — perform the swap that the original reloadNebula would have
	// done. startMu is held, so nothing else can have installed a svc since
	// the haveSvc check above.
	if !inst.setSvc(newSvc) {
		newSvc.Close()
		log.Printf("[renew %s] instance was closed during retry-reload — stopped Nebula", inst.name())
		return true
	}

	if ctrl := newSvc.NebulaControl(); ctrl != nil && inst.endpoint() != "" {
		inst.startWatcher(ctrl)
	}
	inst.reinjectPortmapAddr()
	log.Printf("[renew %s] retry-reload SUCCESS on attempt %d/%d (mode: %s)",
		inst.name(), attempt+1, total, tunMode)

	if inst.onRestart != nil && !inst.isClosed() {
		inst.onRestart(newSvc)
	}
	return true
}

// readEndpointFromDisk reads the control plane endpoint URL from the
// persisted config (set during enrollment). Used by the cold-start path
// in reloadNebula() where agentEndpoint (local to runServe) isn't available.
func readEndpointFromDisk(inst *meshInstance) string {
	data, err := os.ReadFile(filepath.Join(inst.dir(), "endpoint"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// renewalSilenceThreshold is how long lastRenewalActivityAt may sit
// idle before runRenewalWatchdog declares the goroutine dead.
//
// Renewal naturally sleeps for half the cert validity (~12h on a 24h
// cert), but it stamps activity on entry, on each iteration, BEFORE
// sleeping, AFTER waking, and on every retry — so a healthy renewal
// loop produces an activity stamp within ~12h plus a tiny scheduling
// margin. We pick 1/4 of cert validity as the threshold (= 6h on a
// 24h cert) so a real silent death is caught one full activity cycle
// short of the half-validity wake. False positives are nearly
// impossible because the pre-sleep stamp happens BEFORE entering
// time.After() — the watchdog only fires after a multi-hour gap of
// zero log/zero stamp activity.
// Cert validity is 24h (server-side: internal/api/renew.go::renewCertDuration).
// Agent has no compile-time link to that, so we mirror the value here. If
// the server-side validity changes, this constant must update in lockstep.
const expectedCertValidity = 24 * time.Hour
var renewalSilenceThreshold = expectedCertValidity / 4

// renewalWatchdogInterval is how often runRenewalWatchdog wakes to
// check inst.renewalActivityAge(). 5 min is fine — silent deaths
// are detected within 5m of crossing the threshold (= ~6h05m absent
// recovery). The watchdog goroutine is one cheap timer per instance.
const renewalWatchdogInterval = 5 * time.Minute

// runRenewalWatchdog asserts that runCertRenewal is still alive by
// checking inst.lastRenewalActivityAt. When silence exceeds
// renewalSilenceThreshold, it (a) writes a forensic goroutine dump
// to <configDir>/<name>/renewal-stuck-<ts>.txt, (b) logs CRITICAL
// with the silence duration, and (c) invokes inst.restartFn — the
// v0.10.36 lifecycle infrastructure that the stuck-data-plane
// watchdog uses for auto-recovery.
//
// Mirrors the pattern in cmd/agent/keepalive.go's
// `watchdogTrip` — same primitive, different trigger condition.
//
// The watchdog has a cooldown (30 min) after firing so a persistent
// underlying issue can't restart-loop the agent.
func runRenewalWatchdog(ctx context.Context, inst *meshInstance) {
	const cooldown = 30 * time.Minute
	var lastTripAt time.Time
	// Phase EE (v0.11.24): consecutive restartFn failures. After N=3 we
	// escalate via recordRestartFailure → osExitFn(75) for supervisor
	// respawn. See watchdog_escalation.go for the rationale.
	var consecutiveFailures int

	t := time.NewTicker(renewalWatchdogInterval)
	defer t.Stop()
	log.Printf("[renew-watchdog %s] started (threshold=%s, check-interval=%s)",
		inst.name(), renewalSilenceThreshold, renewalWatchdogInterval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		age := inst.renewalActivityAge()
		if age < renewalSilenceThreshold {
			continue
		}
		// Cold-start grace: if lastRenewalActivityAt was never stamped
		// (i.e. age == math.MaxInt64-equivalent), don't fire — the
		// goroutine may simply not have started yet. Bound by a
		// reasonable check: if the stamp is older than 100 years,
		// treat it as "never set".
		if age > 100*365*24*time.Hour {
			continue
		}
		// Cooldown: don't restart-loop on persistent issues.
		if !lastTripAt.IsZero() && time.Since(lastTripAt) < cooldown {
			continue
		}
		if inst.restartCoolingDown() {
			continue
		}
		lastTripAt = time.Now()

		log.Printf("[renew-watchdog %s] CRITICAL: renewal silent for %s (threshold %s) — capturing forensic dump and triggering auto-restart",
			inst.name(), age.Truncate(time.Second), renewalSilenceThreshold)

		writeRenewalStuckDump(inst, age)

		if inst.restartFn != nil {
			if err := inst.restartFn(); err != nil {
				log.Printf("[renew-watchdog %s] auto-restart failed: %v (next attempt in %s)",
					inst.name(), err, cooldown)
				if recordRestartFailure("renew-watchdog", inst.name(), &consecutiveFailures) {
					// Phase EE: osExitFn(75) called; production never
					// returns. Tests inject a no-op fake — bail to
					// avoid spinning.
					return
				}
			} else {
				log.Printf("[renew-watchdog %s] auto-restart triggered", inst.name())
				recordRestartSuccess(&consecutiveFailures)
			}
		} else {
			log.Printf("[renew-watchdog %s] no restartFn wired — agent will not self-recover. Manual `launchctl kickstart` needed.",
				inst.name())
		}
	}
}

// writeRenewalStuckDump writes a goroutine pprof + diagnostic snapshot
// to <configDir>/<name>/renewal-stuck-<ts>.txt. Mirrors the v0.10.36
// stuck-data-plane dump pattern.
func writeRenewalStuckDump(inst *meshInstance, silenceAge time.Duration) {
	ts := time.Now().UTC().Format("20060102T150405Z")
	path := filepath.Join(inst.dir(), fmt.Sprintf("renewal-stuck-%s.txt", ts))

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		log.Printf("[renew-watchdog %s] failed to open dump file %s: %v", inst.name(), path, err)
		return
	}
	defer f.Close()

	fmt.Fprintf(f, "renewal-stuck dump — %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(f, "instance: %s\n", inst.name())
	fmt.Fprintf(f, "silence: %s (threshold %s)\n", silenceAge, renewalSilenceThreshold)
	fmt.Fprintf(f, "endpoint: %s\n", inst.endpoint())
	fmt.Fprintf(f, "node-id: %s\n", inst.nodeID())
	fmt.Fprintf(f, "\n--- goroutine dump ---\n")

	if err := pprof.Lookup("goroutine").WriteTo(f, 2); err != nil {
		fmt.Fprintf(f, "goroutine dump failed: %v\n", err)
	}
	log.Printf("[renew-watchdog %s] forensic dump: %s", inst.name(), path)
}
