// Migration-probe client. Used by `hop-agent migration` to validate that the
// hopssh QUIC transport survives real-world network changes (WiFi → cellular,
// interface drop, sleep/wake).
//
// The probe opens a Session — a long-lived datagram pipe to a fixed server,
// transparently reconnected when the underlying QUIC connection dies. It sends
// one 64-byte datagram per second and expects the server to echo it back.
// Every event is logged with a relative timestamp so we can correlate with
// operator-driven network switches.
//
// Why Session and not quic-go's Connection.AddPath() / Path.Probe() / Switch():
// we tested AddPath/Probe/Switch on real cellular and on `ifconfig en0 down`
// (see spike/migration-evidence/ and the QUIC Connection Migration entry in
// CLAUDE.md). Result: AddPath/Probe is a race-condition fix only — it works
// for sub-30s handoffs while the connection is still alive, but for any real
// outage quic-go silently closes the connection and migration becomes a no-op
// (zero PATH_CHALLENGE frames ever leave the host across many attempts).
// Session reconnects with TLS resumption instead, which IS robust to real
// outages — see internal/quictransport/session.go for the rationale.

package quictransport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/qlog"
)

// ProbeConfig configures a migration probe run.
type ProbeConfig struct {
	// Addr is the QUIC server "host:port" target.
	Addr string
	// Duration is the total run length.
	Duration time.Duration
	// Interval between sent datagrams (default 1s).
	Interval time.Duration
	// Out is where each probe event line is written. If nil, errors out.
	Out io.Writer
	// ForceReconnectAfter, if non-zero, triggers a synthetic reconnect after
	// this delay. Diagnostic only — used to test the reconnect path on a
	// healthy network.
	ForceReconnectAfter time.Duration
}

// RunProbe runs a migration probe. Returns nil on normal completion; an error
// only on unrecoverable setup failure (initial dial). Per-packet errors and
// reconnects are logged but do not abort the run.
func RunProbe(ctx context.Context, cfg ProbeConfig) error {
	if cfg.Interval == 0 {
		cfg.Interval = time.Second
	}
	if cfg.Out == nil {
		return fmt.Errorf("ProbeConfig.Out is required")
	}

	t0 := time.Now()
	logf := func(format string, args ...interface{}) {
		fmt.Fprintf(cfg.Out, "[%6.2fs] ", time.Since(t0).Seconds())
		fmt.Fprintf(cfg.Out, format, args...)
		fmt.Fprintln(cfg.Out)
	}

	logf("MIGRATION PROBE → %s for %v (interval=%v)", cfg.Addr, cfg.Duration, cfg.Interval)
	logf("DIAG: route to server = %s", chosenInterfaceFor(cfg.Addr))
	logf("DIAG: up interfaces = %s", upInterfaces())

	tlsConf := &tls.Config{
		InsecureSkipVerify: true, // probe phase: no auth, see server.go
		NextProtos:         []string{ALPN},
		MinVersion:         tls.VersionTLS13,
		// LRU session cache so reconnects can resume the TLS session
		// (~1 RTT instead of ~3 RTT for full handshake + cert validate).
		ClientSessionCache: tls.NewLRUClientSessionCache(4),
	}
	quicConf := &quic.Config{
		EnableDatagrams:      true,
		MaxIdleTimeout:       60 * time.Second,
		KeepAlivePeriod:      15 * time.Second,
		HandshakeIdleTimeout: 10 * time.Second,
		// qlog: enabled when QLOGDIR env var is set; nil otherwise. Captures
		// the per-connection frame trace for forensic analysis.
		Tracer: qlog.DefaultConnectionTracer,
	}

	serverUDP, err := net.ResolveUDPAddr("udp", cfg.Addr)
	if err != nil {
		logf("RESOLVE FAILED: %v", err)
		return err
	}

	// One UDP socket, shared across reconnects via quic.Transport. Keeping
	// the same local socket means the same source 4-tuple (modulo NAT) for
	// the entire probe run — useful for tcpdump correlation.
	udpConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		logf("LISTEN UDP FAILED: %v", err)
		return err
	}
	defer udpConn.Close()
	tr := &quic.Transport{Conn: udpConn}

	// Session encapsulates the reconnect logic.
	rxCh := make(chan rxEvent, 64)
	rxCtx, rxCancel := context.WithCancel(ctx)
	defer rxCancel()

	sess, err := NewSession(ctx, tr, SessionConfig{
		ServerAddr: serverUDP,
		TLSConfig:  tlsConf,
		QUICConfig: quicConf,
		OnReconnect: func(newConn *quic.Conn) {
			logf("RECONNECTED. local=%s remote=%s connID=%v",
				newConn.LocalAddr(), newConn.RemoteAddr(), connIDPrefix(newConn))
			// Start a fresh receiver goroutine on the new connection.
			// The previous one (if any) returned an error already from its
			// blocked ReceiveDatagram and exited.
			go runReceiver(rxCtx, newConn, rxCh)
		},
	})
	if err != nil {
		logf("DIAL FAILED: %v", err)
		return err
	}
	defer sess.Close()
	conn0, _ := sess.Conn()
	logf("CONNECTED. local=%s remote=%s connID=%v",
		conn0.LocalAddr(), conn0.RemoteAddr(), connIDPrefix(conn0))
	go runReceiver(rxCtx, conn0, rxCh)

	pkt := make([]byte, 64)
	pkt[0] = 0xFF
	var seq uint32
	pending := make(map[uint32]time.Time) // seq → sent_at

	tick := time.NewTicker(cfg.Interval)
	defer tick.Stop()
	end := time.After(cfg.Duration)

	// Watch the local network state every 2s. A change is a strong signal
	// that the existing connection's underlying path may be broken — we
	// proactively reconnect rather than waiting for SendDatagram to fail.
	netCheck := time.NewTicker(2 * time.Second)
	defer netCheck.Stop()
	lastAddrs := localAddrFingerprint()

	// Diagnostic: optional forced reconnect after a fixed delay.
	var forceReconnectCh <-chan time.Time
	if cfg.ForceReconnectAfter > 0 {
		forceReconnectCh = time.After(cfg.ForceReconnectAfter)
		logf("DIAGNOSTIC: will force reconnect after %v", cfg.ForceReconnectAfter)
	}

	var sentCount, rxCount, errCount, reconnectCount uint64

	for {
		select {
		case <-ctx.Done():
			logf("CTX CANCELLED — sent=%d rx=%d err=%d reconnects=%d",
				sentCount, rxCount, errCount, reconnectCount)
			return nil
		case <-end:
			logf("DURATION DONE — sent=%d rx=%d err=%d reconnects=%d",
				sentCount, rxCount, errCount, reconnectCount)
			return nil

		case <-forceReconnectCh:
			logf("FORCED RECONNECT (diagnostic). Triggering...")
			reconnectCount++
			if err := sess.Reconnect(ctx); err != nil {
				logf("FORCED RECONNECT FAILED: %v", err)
			}

		case <-netCheck.C:
			cur := localAddrFingerprint()
			if cur != lastAddrs {
				logf("DIAG: addrs changed. route to server now = %s. interfaces = %s",
					chosenInterfaceFor(cfg.Addr), upInterfaces())
				logf("Triggering proactive reconnect on network change...")
				lastAddrs = cur
				reconnectCount++
				go func() {
					if err := sess.Reconnect(ctx); err != nil {
						logf("PROACTIVE RECONNECT FAILED: %v", err)
					}
				}()
			}

		case <-tick.C:
			seq++
			pkt[1] = byte(seq >> 24)
			pkt[2] = byte(seq >> 16)
			pkt[3] = byte(seq >> 8)
			pkt[4] = byte(seq)
			if err := sess.SendDatagram(pkt); err != nil {
				logf("SEND seq=%d FAILED: %v (reconnect kicked off)", seq, err)
				logf("DIAG: route to server now = %s. interfaces = %s",
					chosenInterfaceFor(cfg.Addr), upInterfaces())
				errCount++
				continue
			}
			pending[seq] = time.Now()
			sentCount++

			// Reap stale pending (>10s with no echo).
			for s, sent := range pending {
				if time.Since(sent) > 10*time.Second {
					logf("LOST seq=%d (no echo after %.1fs)", s, time.Since(sent).Seconds())
					delete(pending, s)
				}
			}

		case ev := <-rxCh:
			if ev.err != nil {
				if errors.Is(ev.err, context.Canceled) {
					return nil
				}
				// Receiver loop exited because the underlying conn closed.
				// SendDatagram on next tick will fail and trigger reconnect;
				// OnReconnect will spawn a new receiver. Just log and wait.
				logf("RECV LOOP EXITED: %v (reconnect should kick in shortly)", ev.err)
				errCount++
				continue
			}
			if sent, ok := pending[ev.seq]; ok {
				rtt := ev.t.Sub(sent).Seconds() * 1000
				if rtt > 200 {
					logf("RECV seq=%d rtt=%.0fms (high)", ev.seq, rtt)
				}
				delete(pending, ev.seq)
			} else {
				logf("RECV seq=%d (no pending — late echo, possibly cross-connection)", ev.seq)
			}
			rxCount++
		}
	}
}

// rxEvent is one decoded echo (or error) from the receiver loop.
type rxEvent struct {
	seq uint32
	t   time.Time
	err error
}

// runReceiver reads echoes from a single quic.Conn and forwards seq events to
// rxCh. Exits with an err event when the connection closes (so the main loop
// knows to expect a reconnect). One runReceiver per connection generation.
func runReceiver(ctx context.Context, conn *quic.Conn, rxCh chan<- rxEvent) {
	for {
		recvCtx, recvCancel := context.WithTimeout(ctx, 30*time.Second)
		msg, err := conn.ReceiveDatagram(recvCtx)
		recvCancel()
		now := time.Now()
		if err != nil {
			select {
			case rxCh <- rxEvent{err: err, t: now}:
			case <-ctx.Done():
			}
			return
		}
		if len(msg) >= 5 && msg[0] == 0xFF {
			seq := uint32(msg[1])<<24 | uint32(msg[2])<<16 | uint32(msg[3])<<8 | uint32(msg[4])
			select {
			case rxCh <- rxEvent{seq: seq, t: now}:
			case <-ctx.Done():
				return
			}
		}
	}
}

// connIDPrefix returns the first 8 hex chars of the connection's source
// connection ID (or "?" if the API isn't available). Useful for distinguishing
// connections in logs across reconnects.
func connIDPrefix(conn *quic.Conn) string {
	// quic-go doesn't expose connection ID via the public API; we approximate
	// by hashing the local addr + remote addr + a short read. For now, return
	// a placeholder — qlog filename has the actual ID.
	if conn == nil {
		return "?"
	}
	return fmt.Sprintf("%s↔%s", conn.LocalAddr(), conn.RemoteAddr())
}

// chosenInterfaceFor returns the local IP the kernel picks when sending UDP
// to dst. We open a throwaway connected UDP socket and read its LocalAddr.
// This is the cleanest cross-platform way to ask "which interface would my
// next packet to this destination go out of right now?"
func chosenInterfaceFor(dst string) string {
	c, err := net.Dial("udp", dst)
	if err != nil {
		return fmt.Sprintf("<dial-fail:%v>", err)
	}
	defer c.Close()
	la := c.LocalAddr().String()
	host, _, _ := net.SplitHostPort(la)
	ip := net.ParseIP(host)
	ifaceName := "?"
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if ipNet, ok := a.(*net.IPNet); ok && ipNet.IP.Equal(ip) {
				ifaceName = iface.Name
				break
			}
		}
	}
	return fmt.Sprintf("%s via %s", la, ifaceName)
}

// upInterfaces returns a comma-separated list of "<name>=<addrs>" for every
// up, non-loopback interface that has at least one address. Used to snapshot
// the local network state for diagnostic logging.
func upInterfaces() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "<error>"
	}
	var sb strings.Builder
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		if len(addrs) == 0 {
			continue
		}
		sb.WriteString(iface.Name)
		sb.WriteByte('[')
		for i, a := range addrs {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(a.String())
		}
		sb.WriteByte(']')
		sb.WriteByte(' ')
	}
	return sb.String()
}

// localAddrFingerprint returns a deterministic string of all up-and-non-loopback
// interface addresses. Comparing two fingerprints over time tells us when the
// local network configuration changed (interface dropped, new IP, etc.).
func localAddrFingerprint() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var sb strings.Builder
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			sb.WriteString(addr.String())
			sb.WriteByte(',')
		}
	}
	return sb.String()
}
