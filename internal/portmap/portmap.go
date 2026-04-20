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

	mu      sync.Mutex
	winner  Client
	current netip.AddrPort
	ttl     time.Duration
	onChng  ChangeHandler

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

	winner, addr, ttl := m.probe(ctx, clients)
	if winner == nil {
		m.l.Info("portmap: no protocol available; falling back to hole-punching only")
		<-ctx.Done()
		return
	}

	m.mu.Lock()
	m.winner = winner
	prev := m.current
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

	m.refreshLoop(ctx, winner)
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

func (m *Manager) refreshLoop(ctx context.Context, winner Client) {
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
			return
		case <-time.After(sleep):
		}

		rctx, rcancel := context.WithTimeout(ctx, m.probeTimeout)
		addr, newTTL, err := winner.Map(rctx, m.internalPort)
		rcancel()
		if err != nil {
			m.l.WithError(err).WithField("protocol", winner.Name()).
				Warn("portmap: refresh failed; holding last mapping")
			continue
		}

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
