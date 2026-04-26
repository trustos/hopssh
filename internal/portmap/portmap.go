package portmap

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// Client is implemented by each router-mapping protocol (NAT-PMP, PCP,
// UPnP). The coordinator probes all available clients in parallel and
// keeps the first one that successfully returns a mapping.
type Client interface {
	// Map requests a port mapping from the router. Returns the public
	// AddrPort the router assigned, the mapping lifetime (for refresh
	// scheduling), or an error. A nil error with zero ttl means the
	// router didn't report a lifetime — caller should pick a safe default.
	Map(ctx context.Context, internalPort uint16) (public netip.AddrPort, ttl time.Duration, err error)

	// Unmap removes a previously-requested mapping. Best-effort; routers
	// will eventually drop it by lifetime expiration anyway. Called on
	// graceful shutdown.
	Unmap(ctx context.Context, internalPort uint16) error

	// Name identifies the protocol for logs and metrics ("natpmp", "pcp",
	// "upnp"). Must be stable across process lifetime.
	Name() string
}

// ChangeHandler is invoked when the public mapping address changes on a
// refresh (unusual but possible — router reassigns external port). Caller
// uses this to update the lighthouse's advertised-addr list.
type ChangeHandler func(old, new netip.AddrPort)

// Manager is the portmap coordinator. One instance per hop-agent; probes
// all available protocols at startup, keeps the first winner, refreshes
// the mapping before the router's TTL expires.
type Manager struct {
	l            *logrus.Logger
	internalPort uint16

	// Options — injected at construction for testability.
	probeTimeout time.Duration
	clients      []Client // if nil, defaults are built in Start()

	// retryBackoff is the sequence of sleep durations between probe
	// retries when no protocol has succeeded yet. After the sequence is
	// exhausted the last value is repeated indefinitely (ceiling).
	// Exposed for tests to shrink.
	retryBackoff []time.Duration

	mu      sync.Mutex
	winner  Client
	current netip.AddrPort
	ttl     time.Duration
	onChng  ChangeHandler

	// reprobe is a buffered signal channel. External callers (the
	// network-change watcher) drop a value to request an immediate
	// re-probe — woken goroutine drains it. Buffered 1 so multiple
	// concurrent signals collapse into one probe.
	reprobe chan struct{}

	cancel context.CancelFunc
	done   chan struct{}
}

// New returns an inactive Manager. Call Start() to probe + refresh in a
// background goroutine.
func New(l *logrus.Logger, internalPort uint16) *Manager {
	if l == nil {
		l = logrus.StandardLogger()
	}
	return &Manager{
		l:            l,
		internalPort: internalPort,
		probeTimeout: 3 * time.Second,
		// Exponential-ish backoff capped at 5 min. The initial probe
		// may fail transiently (laptop just woken up, router still
		// bringing up UPnP, DHCP not finished). Subsequent retries
		// pick up once the router is actually reachable. Without this
		// an MBP observed in prod kept portmap=dead for 24 h after a
		// single startup-time failure.
		retryBackoff: []time.Duration{
			30 * time.Second,
			1 * time.Minute,
			2 * time.Minute,
			5 * time.Minute,
		},
		reprobe: make(chan struct{}, 1),
	}
}

// ReProbe asks the background goroutine to drop the current mapping (if
// any) and re-run the protocol probe. Non-blocking: if a re-probe is
// already queued, the call collapses into the pending one. Safe to call
// from any goroutine. Used by the agent's network-change watcher so a
// WiFi↔cellular swap or sleep/wake cycle forces portmap to rediscover
// protocol availability and the router's external IP:port.
func (m *Manager) ReProbe() {
	select {
	case m.reprobe <- struct{}{}:
	default:
	}
}

// OnChange registers a handler that is called when the public mapping
// changes (including the initial establishment). Must be called before
// Start.
func (m *Manager) OnChange(h ChangeHandler) {
	m.onChng = h
}

// SetClients injects a custom client list (tests). Production code uses
// the defaults built inside Start.
func (m *Manager) SetClients(clients []Client) {
	m.clients = clients
}

// Current returns the most recent successful public mapping. Zero-value
// AddrPort if no mapping has been established yet.
func (m *Manager) Current() netip.AddrPort {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

// LifetimeSeconds returns the router-reported lease lifetime of the current
// mapping, in seconds. 0 if no mapping is established or if the protocol
// didn't include a lifetime hint (some routers lie). Callers use this to
// tag the endpoint with an expiry timestamp when distributing it via the
// control plane (Layer 1 — RFC 6886-aligned TTL propagation), so other
// peers can prune stale entries proactively when the lease elapses.
//
// Reflects the most-recent successful probe; rotates with each refresh.
func (m *Manager) LifetimeSeconds() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ttl <= 0 {
		return 0
	}
	return int(m.ttl.Seconds())
}

// Start probes for a port mapping in the background. Returns nil once
// the background goroutine is running; the actual mapping may or may
// not be established by the time Start returns. Callers use OnChange to
// be notified when a mapping lands (or Current() to poll).
//
// If no protocol succeeds within probeTimeout, the Manager stays passive
// and logs an info-level message. Subsequent Start calls are no-ops.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		return errors.New("portmap: Start already called")
	}

	clients := m.clients
	if clients == nil {
		// Production defaults — three protocols probed in parallel; the
		// coordinator keeps the first that returns. NAT-PMP and PCP
		// share UDP 5351 on the gateway and have similar latency
		// characteristics; UPnP discovers via SSDP multicast (slower
		// but covers more routers). Combined coverage approaches 100%
		// of consumer routers that allow ANY port-mapping protocol.
		gw, gwErr := DiscoverGateway()
		clients = []Client{}
		if gwErr == nil {
			clients = append(clients, NewNATPMP(gw))
			clients = append(clients, NewPCP(gw))
		}
		clients = append(clients, NewUPnP())
		if len(clients) == 0 {
			m.mu.Unlock()
			return fmt.Errorf("portmap: no clients available (gw discovery: %w)", gwErr)
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.done = make(chan struct{})
	m.mu.Unlock()

	go m.run(runCtx, clients)
	return nil
}

// Stop cancels the background goroutine and attempts a best-effort unmap.
// Safe to call multiple times; subsequent calls are no-ops.
func (m *Manager) Stop() {
	m.mu.Lock()
	cancel := m.cancel
	done := m.done
	winner := m.winner
	m.cancel = nil
	m.mu.Unlock()

	if cancel == nil {
		return
	}
	cancel()
	<-done

	if winner != nil {
		ctx, c := context.WithTimeout(context.Background(), 2*time.Second)
		defer c()
		if err := winner.Unmap(ctx, m.internalPort); err != nil {
			m.l.WithError(err).WithField("protocol", winner.Name()).Debug("portmap: unmap on stop")
		}
	}
}

func (m *Manager) run(ctx context.Context, clients []Client) {
	defer close(m.done)

	for {
		winner, addr, ttl := m.probe(ctx, clients)
		if winner != nil {
			m.onProbeSuccess(winner, addr, ttl)
			// refreshLoop returns when ctx is cancelled OR a re-probe
			// signal is observed. In the re-probe case we fall through
			// the outer loop and probe again from scratch.
			stopped := m.refreshLoop(ctx, winner)
			if stopped {
				return
			}
			// Re-probe requested: clear current winner so refresh
			// stops relying on it, then loop back to probe().
			m.clearWinner()
			continue
		}

		// No protocol succeeded this round. Sleep a backoff and retry.
		// Previously (pre-v0.10.11) the goroutine parked on <-ctx.Done()
		// here forever — one transient probe failure at startup
		// permanently silenced portmap. Retry indefinitely instead.
		m.l.Info("portmap: no protocol available; falling back to hole-punching only (will retry)")
		if !m.sleepOrReprobe(ctx, m.retryAttemptDelay(0)) {
			return
		}

		for attempt := 1; ; attempt++ {
			winner, addr, ttl := m.probe(ctx, clients)
			if winner != nil {
				m.l.WithField("attempt", attempt).Info("portmap: probe succeeded after retry")
				m.onProbeSuccess(winner, addr, ttl)
				if stopped := m.refreshLoop(ctx, winner); stopped {
					return
				}
				m.clearWinner()
				break
			}
			if ctx.Err() != nil {
				return
			}
			if !m.sleepOrReprobe(ctx, m.retryAttemptDelay(attempt)) {
				return
			}
		}
	}
}

// onProbeSuccess records a winning probe result and invokes the change
// handler (lighthouse advertise_addr injection).
func (m *Manager) onProbeSuccess(winner Client, addr netip.AddrPort, ttl time.Duration) {
	m.mu.Lock()
	prev := m.current
	m.winner = winner
	m.current = addr
	m.ttl = ttl
	handler := m.onChng
	m.mu.Unlock()

	m.l.WithFields(logrus.Fields{
		"protocol": winner.Name(),
		"public":   addr.String(),
		"ttl":      ttl,
	}).Info("portmap: established public mapping")

	if handler != nil {
		handler(prev, addr)
	}
}

// clearWinner drops the cached winner + current mapping. Called when a
// re-probe is requested so a stale mapping doesn't linger if the new
// probe chooses a different protocol or address.
func (m *Manager) clearWinner() {
	m.mu.Lock()
	m.winner = nil
	m.current = netip.AddrPort{}
	m.ttl = 0
	m.mu.Unlock()
}

// retryAttemptDelay returns the backoff for the Nth failed attempt.
// Attempts beyond len(retryBackoff)-1 all use the last (capped) value.
func (m *Manager) retryAttemptDelay(attempt int) time.Duration {
	if len(m.retryBackoff) == 0 {
		return 30 * time.Second
	}
	if attempt >= len(m.retryBackoff) {
		return m.retryBackoff[len(m.retryBackoff)-1]
	}
	return m.retryBackoff[attempt]
}

// sleepOrReprobe blocks until the delay elapses, a re-probe is signaled,
// or the context is cancelled. Returns true if the caller should
// continue (delay elapsed OR re-probe signaled); false if ctx cancelled.
func (m *Manager) sleepOrReprobe(ctx context.Context, delay time.Duration) bool {
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	case <-m.reprobe:
		return true
	}
}

// probe returns the first client that succeeds, or (nil, zero, 0) if
// none did within probeTimeout.
func (m *Manager) probe(ctx context.Context, clients []Client) (Client, netip.AddrPort, time.Duration) {
	probeCtx, cancel := context.WithTimeout(ctx, m.probeTimeout)
	defer cancel()

	type result struct {
		client Client
		addr   netip.AddrPort
		ttl    time.Duration
	}
	ch := make(chan result, len(clients))
	for _, c := range clients {
		go func(c Client) {
			addr, ttl, err := c.Map(probeCtx, m.internalPort)
			if err != nil {
				m.l.WithError(err).WithField("protocol", c.Name()).Debug("portmap: probe failed")
				return
			}
			select {
			case ch <- result{c, addr, ttl}:
			case <-probeCtx.Done():
			}
		}(c)
	}

	select {
	case r := <-ch:
		return r.client, r.addr, r.ttl
	case <-probeCtx.Done():
		return nil, netip.AddrPort{}, 0
	}
}

// refreshLoop keeps the mapping alive. Returns true if the loop
// exited because ctx was cancelled (caller should return immediately).
// Returns false if a re-probe was requested OR refresh has failed for
// too long — caller should drop the current winner and re-run probe().
func (m *Manager) refreshLoop(ctx context.Context, winner Client) bool {
	const refreshFailureLimit = 3
	refreshFailures := 0

	for {
		m.mu.Lock()
		ttl := m.ttl
		m.mu.Unlock()

		// Refresh at 50% of ttl, floor at 60 s so a misbehaving router
		// returning ttl=0 or very small can't create a tight busy-loop.
		sleep := ttl / 2
		if sleep < time.Minute {
			sleep = time.Minute
		}
		select {
		case <-ctx.Done():
			return true
		case <-time.After(sleep):
		case <-m.reprobe:
			m.l.Info("portmap: re-probe requested, dropping current mapping")
			return false
		}

		rctx, rcancel := context.WithTimeout(ctx, m.probeTimeout)
		addr, newTTL, err := winner.Map(rctx, m.internalPort)
		rcancel()
		if err != nil {
			refreshFailures++
			m.l.WithError(err).
				WithField("protocol", winner.Name()).
				WithField("consecutive_failures", refreshFailures).
				Warn("portmap: refresh failed; holding last mapping")
			if refreshFailures >= refreshFailureLimit {
				m.l.WithField("protocol", winner.Name()).
					Info("portmap: refresh failed repeatedly, re-probing from scratch")
				return false
			}
			continue
		}
		refreshFailures = 0

		m.mu.Lock()
		prev := m.current
		changed := prev != addr
		m.current = addr
		m.ttl = newTTL
		handler := m.onChng
		m.mu.Unlock()

		if changed {
			m.l.WithFields(logrus.Fields{
				"protocol": winner.Name(),
				"was":      prev.String(),
				"now":      addr.String(),
			}).Info("portmap: external address changed on refresh")
			if handler != nil {
				handler(prev, addr)
			}
		}
	}
}
