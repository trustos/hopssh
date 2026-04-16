// Migration-probe client. Used by `hop-agent migration` to validate that a
// QUIC connection survives a local IP change (WiFi → cellular handoff).
//
// The probe opens a long-lived QUIC connection, sends one 64-byte datagram per
// second, and expects the server to echo it back. Every event (send, recv,
// error, local-address change indicating QUIC migration) is logged with a
// timestamp so we can correlate with operator-driven network switches.

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
	// Out is where each probe event line is written. If nil, uses os.Stderr.
	Out io.Writer
	// ForceMigrateAfter, if non-zero, triggers a forced migration attempt
	// after this delay. Diagnostic only — used to test the migration mechanism
	// in a controlled setting (e.g. on LAN, where there's no NAT to confuse).
	ForceMigrateAfter time.Duration
}

// RunProbe runs a migration probe. Returns nil on normal completion; an error
// on dial failure (we keep going past per-packet errors so we can observe
// migration recovery).
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
	}
	quicConf := &quic.Config{
		EnableDatagrams:      true,
		MaxIdleTimeout:       60 * time.Second,
		KeepAlivePeriod:      15 * time.Second,
		HandshakeIdleTimeout: 10 * time.Second,
		// qlog: enabled when QLOGDIR env var is set; nil otherwise. Used to
		// capture PATH_CHALLENGE/PATH_RESPONSE frames during migration debug.
		Tracer: qlog.DefaultConnectionTracer,
	}

	// Use an explicit Transport (rather than quic.DialAddr) so we have a handle
	// on the underlying UDP socket. quic-go's connection migration is
	// client-triggered: when our local network changes, we have to open a new
	// socket, AddPath() it to the connection, Probe it, then Switch.
	serverUDP, err := net.ResolveUDPAddr("udp", cfg.Addr)
	if err != nil {
		logf("RESOLVE FAILED: %v", err)
		return err
	}
	// Stash for migration helper.
	migrateServerAddr := serverUDP

	initConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		logf("LISTEN UDP FAILED: %v", err)
		return err
	}
	tr := &quic.Transport{Conn: initConn}

	dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
	conn, err := tr.Dial(dialCtx, serverUDP, tlsConf, quicConf)
	dialCancel()
	if err != nil {
		logf("DIAL FAILED: %v", err)
		_ = initConn.Close()
		return err
	}
	logf("CONNECTED. local=%s remote=%s", conn.LocalAddr(), conn.RemoteAddr())
	defer func() { _ = conn.CloseWithError(0, "done") }()

	// Receiver goroutine — feeds events back through rxCh.
	type rxEvent struct {
		seq uint32
		t   time.Time
		err error
	}
	rxCh := make(chan rxEvent, 64)
	rxCtx, rxCancel := context.WithCancel(ctx)
	defer rxCancel()
	go func() {
		for {
			recvCtx, recvCancel := context.WithTimeout(rxCtx, 30*time.Second)
			msg, err := conn.ReceiveDatagram(recvCtx)
			recvCancel()
			now := time.Now()
			if err != nil {
				select {
				case rxCh <- rxEvent{err: err, t: now}:
				case <-rxCtx.Done():
				}
				return
			}
			if len(msg) >= 5 && msg[0] == 0xFF {
				seq := uint32(msg[1])<<24 | uint32(msg[2])<<16 | uint32(msg[3])<<8 | uint32(msg[4])
				select {
				case rxCh <- rxEvent{seq: seq, t: now}:
				case <-rxCtx.Done():
					return
				}
			}
		}
	}()

	pkt := make([]byte, 64)
	pkt[0] = 0xFF
	var seq uint32
	pending := make(map[uint32]time.Time) // seq → sent_at
	lastLocal := conn.LocalAddr().String()

	tick := time.NewTicker(cfg.Interval)
	defer tick.Stop()
	end := time.After(cfg.Duration)

	// Watch the local network for changes (every 2s). When the active
	// interface fingerprint changes, attempt QUIC connection migration:
	// open a new UDP socket, AddPath, Probe, Switch.
	//
	// We ALSO retry migration on persistent send errors — the address
	// fingerprint changes once when the old interface dies but doesn't
	// change again when the new interface stabilizes seconds later. So we
	// need a fallback trigger: "if I've had N consecutive send errors,
	// the network has probably changed, try migrating regardless."
	netCheck := time.NewTicker(2 * time.Second)
	defer netCheck.Stop()
	lastAddrs := localAddrFingerprint()
	consecSendErrors := 0
	lastMigrationAttempt := time.Time{}

	// Diagnostic: optional forced migration after a fixed delay.
	var forceMigrateCh <-chan time.Time
	if cfg.ForceMigrateAfter > 0 {
		forceMigrateCh = time.After(cfg.ForceMigrateAfter)
		logf("DIAGNOSTIC: will force migration after %v", cfg.ForceMigrateAfter)
	}

	var sentCount, rxCount, errCount uint64

	for {
		select {
		case <-ctx.Done():
			logf("CTX CANCELLED — sent=%d rx=%d err=%d", sentCount, rxCount, errCount)
			return nil
		case <-end:
			logf("DURATION DONE — sent=%d rx=%d err=%d", sentCount, rxCount, errCount)
			return nil

		case <-forceMigrateCh:
			logf("FORCED MIGRATION (diagnostic). Attempting...")
			lastMigrationAttempt = time.Now()
			if err := migrate(ctx, conn, migrateServerAddr, logf); err != nil {
				logf("FORCED MIGRATION FAILED: %v", err)
			} else {
				logf("FORCED MIGRATION SUCCESS. local now=%s", conn.LocalAddr())
			}

		case <-netCheck.C:
			cur := localAddrFingerprint()
			addrChanged := cur != lastAddrs
			errStorm := consecSendErrors >= 3
			if addrChanged {
				logf("DIAG: addrs changed. route to server now = %s. interfaces = %s",
					chosenInterfaceFor(cfg.Addr), upInterfaces())
			}
			// Throttle migration attempts — at least 3s between tries.
			cooldown := time.Since(lastMigrationAttempt) < 3*time.Second
			if (addrChanged || errStorm) && !cooldown {
				reason := "addrs changed"
				if errStorm && !addrChanged {
					reason = fmt.Sprintf("%d consecutive send errors", consecSendErrors)
				}
				logf("NETWORK CHANGE / FAILURE detected (%s). Attempting QUIC migration...", reason)
				lastAddrs = cur
				lastMigrationAttempt = time.Now()
				if err := migrate(ctx, conn, migrateServerAddr, logf); err != nil {
					logf("MIGRATION FAILED: %v", err)
				} else {
					logf("MIGRATION SUCCESS. local now=%s", conn.LocalAddr())
					consecSendErrors = 0
				}
			}

		case <-tick.C:
			// Detect local-address change (signals QUIC migration fired).
			cur := conn.LocalAddr().String()
			if cur != lastLocal {
				logf("LOCAL ADDR CHANGED: %s → %s (QUIC migration may have fired)", lastLocal, cur)
				lastLocal = cur
			}

			seq++
			pkt[1] = byte(seq >> 24)
			pkt[2] = byte(seq >> 16)
			pkt[3] = byte(seq >> 8)
			pkt[4] = byte(seq)
			if err := conn.SendDatagram(pkt); err != nil {
				logf("SEND seq=%d FAILED: %v", seq, err)
				if consecSendErrors == 0 {
					// Only on first failure of a streak, log the interface
					// state — avoids log spam during sustained outages.
					logf("DIAG: route to server now = %s. interfaces = %s",
						chosenInterfaceFor(cfg.Addr), upInterfaces())
				}
				errCount++
				consecSendErrors++
				continue
			}
			pending[seq] = time.Now()
			sentCount++
			consecSendErrors = 0

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
				logf("RECV ERROR: %v (typically means QUIC connection died)", ev.err)
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
				logf("RECV seq=%d (no pending — late echo)", ev.seq)
			}
			rxCount++
		}
	}
}

// migrate moves the QUIC connection to a freshly-opened UDP socket on the
// current default route. Two-phase to handle the WiFi → cellular handoff
// gracefully:
//
//  1. PRE-FLIGHT — open a candidate socket and try sending a tiny test
//     packet to the server. If the kernel returns "network is unreachable"
//     (cellular still negotiating, route table not committed, etc.), close
//     the socket, wait, and retry. Only proceed once a packet actually
//     leaves the host.
//
//  2. AddPath / Probe / Switch — once the path is sendable, do the
//     standard QUIC migration handshake.
//
// This is what quic-go does NOT do automatically — its migration support
// requires the caller to provide a usable Transport.
func migrate(ctx context.Context, conn *quic.Conn, serverAddr *net.UDPAddr, logf func(format string, args ...interface{})) error {
	// --- Phase 1: pre-flight ---
	// Poll until we can open a UDP socket and send to the server. Cap at 60s
	// of waiting — cellular handoff usually settles in 5-30s but we give
	// margin for slow networks.
	var newConn *net.UDPConn
	deadline := time.Now().Add(60 * time.Second)
	backoff := 250 * time.Millisecond
	preflightStart := time.Now()
	for time.Now().Before(deadline) {
		c, err := net.ListenUDP("udp", nil)
		if err != nil {
			time.Sleep(backoff)
			continue
		}
		// Send 10 small sentinel packets over ~500ms. The server drops
		// anything that isn't a valid QUIC packet (harmless), but the
		// burst forces cellular CGNAT to commit a stable outbound mapping
		// for this 4-tuple. A single test packet is not enough on some
		// carriers — the mapping is ephemeral until traffic continues.
		burstOK := false
		for i := 0; i < 10; i++ {
			_, err = c.WriteToUDP([]byte{0}, serverAddr)
			if err != nil {
				break
			}
			burstOK = true
			time.Sleep(50 * time.Millisecond)
		}
		if burstOK {
			// Settle period. Lets the carrier finalize its NAT state
			// before the larger PATH_CHALLENGE packet goes out.
			time.Sleep(1 * time.Second)
			newConn = c
			logf("  path usable after %v (10x preflight + 1s settle): local=%s", time.Since(preflightStart).Round(10*time.Millisecond), c.LocalAddr())
			break
		}
		_ = c.Close()
		// Common errors during handoff: "network is unreachable",
		// "no route to host". Both mean "wait, try again".
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 4*time.Second {
			backoff *= 2
		}
	}
	if newConn == nil {
		return fmt.Errorf("no usable path within %v (cellular never came up?)", 60*time.Second)
	}

	// --- Phase 2: register + validate + switch ---
	newTr := &quic.Transport{Conn: newConn}
	path, err := conn.AddPath(newTr)
	if err != nil {
		_ = newConn.Close()
		return fmt.Errorf("AddPath: %w", err)
	}

	// 15s should now be plenty since the path is verified-sendable.
	probeCtx, probeCancel := context.WithTimeout(ctx, 15*time.Second)
	if err := path.Probe(probeCtx); err != nil {
		probeCancel()
		_ = path.Close()
		_ = newConn.Close()
		return fmt.Errorf("Probe: %w", err)
	}
	probeCancel()
	logf("  PATH_CHALLENGE validated")

	if err := path.Switch(); err != nil {
		_ = path.Close()
		_ = newConn.Close()
		return fmt.Errorf("Switch: %w", err)
	}
	return nil
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
	// Map the local IP back to an interface name so the log is readable.
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
// up, non-loopback interface. Used to snapshot the local network state.
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

