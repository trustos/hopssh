// Session wraps a quic.Conn with transparent reconnect on connection death.
//
// Why this exists: quic-go closes the connection unconditionally on any
// non-EMSGSIZE socket write error (send_queue.go:90-96), and once closed it
// cannot be revived — RFC 9000 §10 makes connections terminal after
// CONNECTION_CLOSE. Connection migration (AddPath/Probe/Switch) only works
// while the connection is still alive, so it handles sub-30s race-condition
// handoffs but NOT real-world network outages (verified empirically with
// `ifconfig en0 down` for >50s — see spike/migration-evidence/ and the
// QUIC Connection Migration entry in CLAUDE.md's discovery log).
//
// What this provides: a Session looks like a long-lived datagram pipe to a
// fixed server, but under the hood the underlying QUIC connection may be
// torn down and re-established at any time. TLS session resumption keeps
// the reconnect cost down to ~1 RTT (vs ~3 RTT for a cold handshake) and
// avoids re-doing certificate validation.
package quictransport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
)

// SessionConfig configures a Session.
type SessionConfig struct {
	// ServerAddr is the resolved UDP endpoint we keep reconnecting to.
	ServerAddr *net.UDPAddr
	// TLSConfig should have a ClientSessionCache set for resumption to work.
	// If nil, reconnects pay the full handshake cost every time.
	TLSConfig *tls.Config
	// QUICConfig as passed to quic.Transport.Dial.
	QUICConfig *quic.Config
	// DialTimeout caps how long a single reconnect attempt may run.
	// Default 10s.
	DialTimeout time.Duration
	// ReconnectBackoff is the initial sleep between failed reconnects.
	// Doubles up to a 4s cap. Default 250ms.
	ReconnectBackoff time.Duration
	// OnReconnect, if set, is called after each successful reconnect with
	// the new connection. Used by the probe to log + reset receiver loops.
	OnReconnect func(newConn *quic.Conn)
}

// Session keeps a QUIC connection to a fixed server, transparently reopening
// it when the underlying connection dies. Safe for concurrent use.
type Session struct {
	cfg       SessionConfig
	transport *quic.Transport

	mu      sync.RWMutex
	conn    *quic.Conn
	connGen uint64

	reconnecting atomic.Bool
	closed       atomic.Bool
}

// NewSession opens the initial connection and returns a Session ready for use.
// The caller owns the transport and must keep it alive for the Session's
// lifetime (we share it across reconnects so the local UDP socket — and
// therefore the local source 4-tuple visible to NAT — stays stable).
func NewSession(ctx context.Context, transport *quic.Transport, cfg SessionConfig) (*Session, error) {
	if cfg.ServerAddr == nil {
		return nil, fmt.Errorf("SessionConfig.ServerAddr is required")
	}
	if cfg.TLSConfig == nil {
		return nil, fmt.Errorf("SessionConfig.TLSConfig is required")
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	if cfg.ReconnectBackoff == 0 {
		cfg.ReconnectBackoff = 250 * time.Millisecond
	}
	// Force a ClientSessionCache if the caller didn't set one. Without this
	// every reconnect pays the full handshake cost.
	if cfg.TLSConfig.ClientSessionCache == nil {
		cfg.TLSConfig = cfg.TLSConfig.Clone()
		cfg.TLSConfig.ClientSessionCache = tls.NewLRUClientSessionCache(4)
	}

	s := &Session{cfg: cfg, transport: transport}

	dialCtx, cancel := context.WithTimeout(ctx, cfg.DialTimeout)
	defer cancel()
	conn, err := transport.Dial(dialCtx, cfg.ServerAddr, cfg.TLSConfig, cfg.QUICConfig)
	if err != nil {
		return nil, fmt.Errorf("initial dial: %w", err)
	}
	s.conn = conn
	s.connGen = 1
	return s, nil
}

// Conn returns the current underlying connection and its generation counter.
// Use the generation to detect reconnects between successive reads/writes
// (e.g. a ReceiveDatagram loop should restart when the gen changes).
func (s *Session) Conn() (*quic.Conn, uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.conn, s.connGen
}

// SendDatagram sends one unreliable datagram on the current connection.
// On error, triggers an asynchronous reconnect and returns the error to the
// caller; the caller decides whether to retry or drop the packet.
func (s *Session) SendDatagram(b []byte) error {
	if s.closed.Load() {
		return errors.New("session closed")
	}
	s.mu.RLock()
	conn := s.conn
	s.mu.RUnlock()
	if conn == nil {
		go s.reconnectAsync(context.Background())
		return errors.New("no current connection (reconnect pending)")
	}
	if err := conn.SendDatagram(b); err != nil {
		// Connection is probably toast. Trigger an async reconnect; the
		// caller will see this error, drop the packet, and try again on
		// its next tick (by which time we'll hopefully have a new conn).
		go s.reconnectAsync(context.Background())
		return err
	}
	return nil
}

// ReceiveDatagram reads one datagram from the current connection. If the
// connection is reconnected mid-read, this returns the underlying error and
// the caller can retry — the next call uses the new connection.
func (s *Session) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	if s.closed.Load() {
		return nil, errors.New("session closed")
	}
	s.mu.RLock()
	conn := s.conn
	s.mu.RUnlock()
	if conn == nil {
		return nil, errors.New("no current connection")
	}
	return conn.ReceiveDatagram(ctx)
}

// Reconnect forces an immediate reconnect. Useful when the caller has
// out-of-band knowledge that the network changed (e.g. interface fingerprint
// shifted) and wants to swap connections proactively rather than waiting
// for SendDatagram to fail.
func (s *Session) Reconnect(ctx context.Context) error {
	return s.reconnectSync(ctx)
}

// reconnectAsync runs a reconnect in the background, single-flight.
func (s *Session) reconnectAsync(ctx context.Context) {
	if !s.reconnecting.CompareAndSwap(false, true) {
		return
	}
	defer s.reconnecting.Store(false)
	_ = s.reconnectLoop(ctx)
}

// reconnectSync runs a reconnect synchronously, single-flight (returns
// immediately if another reconnect is in progress).
func (s *Session) reconnectSync(ctx context.Context) error {
	if !s.reconnecting.CompareAndSwap(false, true) {
		return errors.New("reconnect already in progress")
	}
	defer s.reconnecting.Store(false)
	return s.reconnectLoop(ctx)
}

// reconnectLoop tries to dial a new connection, retrying with backoff. Single
// caller is guaranteed by reconnecting atomic guard.
func (s *Session) reconnectLoop(ctx context.Context) error {
	// Tear down the old connection cleanly so the server isn't holding state.
	s.mu.Lock()
	old := s.conn
	s.conn = nil
	s.mu.Unlock()
	if old != nil {
		_ = old.CloseWithError(0, "session reconnect")
	}

	backoff := s.cfg.ReconnectBackoff
	for {
		if s.closed.Load() {
			return errors.New("session closed during reconnect")
		}
		dialCtx, cancel := context.WithTimeout(ctx, s.cfg.DialTimeout)
		conn, err := s.transport.Dial(dialCtx, s.cfg.ServerAddr, s.cfg.TLSConfig, s.cfg.QUICConfig)
		cancel()
		if err == nil {
			s.mu.Lock()
			s.conn = conn
			s.connGen++
			gen := s.connGen
			s.mu.Unlock()
			if s.cfg.OnReconnect != nil {
				s.cfg.OnReconnect(conn)
			}
			_ = gen
			return nil
		}
		// Sleep + backoff (capped at 4s). Honor ctx cancellation.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 4*time.Second {
			backoff *= 2
		}
	}
}

// Close ends the session permanently and closes the current connection.
// Subsequent SendDatagram / ReceiveDatagram calls return an error.
func (s *Session) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	s.mu.Lock()
	conn := s.conn
	s.conn = nil
	s.mu.Unlock()
	if conn != nil {
		return conn.CloseWithError(0, "session close")
	}
	return nil
}
