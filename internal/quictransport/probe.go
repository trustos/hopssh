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
	"time"

	"github.com/quic-go/quic-go"
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
	}

	dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
	conn, err := quic.DialAddr(dialCtx, cfg.Addr, tlsConf, quicConf)
	dialCancel()
	if err != nil {
		logf("DIAL FAILED: %v", err)
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

	var sentCount, rxCount, errCount uint64

	for {
		select {
		case <-ctx.Done():
			logf("CTX CANCELLED — sent=%d rx=%d err=%d", sentCount, rxCount, errCount)
			return nil
		case <-end:
			logf("DURATION DONE — sent=%d rx=%d err=%d", sentCount, rxCount, errCount)
			return nil

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
