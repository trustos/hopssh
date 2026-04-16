// QUIC datagram throughput + latency spike.
//
// Two roles:
//   server: listens on UDP/4242, accepts QUIC connections, echoes datagrams
//   client: connects to server, sends datagrams as fast as possible (or paced)
//
// Goal: validate that quic-go's datagram path can hit the throughput we get
// with our current Nebula+sendmsg_x patches (~150-300 Mbps on macOS).
//
// Usage:
//   ./quic-spike server :4242
//   ./quic-spike client <peer-ip>:4242 <duration-sec> <packet-size-bytes>
//   ./quic-spike rttprobe <peer-ip>:4242 <duration-sec> <interval-ms>
//   ./quic-spike migration <peer-ip>:4242 <duration-sec>
//
// The "migration" mode opens a long-lived QUIC connection, sends/echoes 1
// datagram/sec, and logs every event with timestamps. Designed to capture
// what happens when the local IP changes mid-stream (network switch).
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	mrand "math/rand"
	"os"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
)

const alpn = "hopssh-quic-spike"

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: quic-spike {server|client|rttprobe} ...")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "server":
		runServer(os.Args[2])
	case "client":
		dur, _ := strconv.Atoi(os.Args[3])
		size, _ := strconv.Atoi(os.Args[4])
		runClient(os.Args[2], time.Duration(dur)*time.Second, size)
	case "rttprobe":
		dur, _ := strconv.Atoi(os.Args[3])
		intervalMs, _ := strconv.Atoi(os.Args[4])
		runRTTProbe(os.Args[2], time.Duration(dur)*time.Second, time.Duration(intervalMs)*time.Millisecond)
	case "migration":
		dur, _ := strconv.Atoi(os.Args[3])
		runMigrationProbe(os.Args[2], time.Duration(dur)*time.Second)
	default:
		log.Fatalf("unknown command: %s", os.Args[1])
	}
}

// ---------- TLS / cert helpers (self-signed for spike) ----------

func generateTLSConfig(server bool) *tls.Config {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "hopssh-spike"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	if server {
		tmpl.IPAddresses = nil // don't care for spike
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		log.Fatal(err)
	}
	pemCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, _ := x509.MarshalPKCS8PrivateKey(priv)
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(pemCert, pemKey)
	if err != nil {
		log.Fatal(err)
	}
	return &tls.Config{
		Certificates:       []tls.Certificate{cert},
		NextProtos:         []string{alpn},
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
	}
}

func quicConfig() *quic.Config {
	return &quic.Config{
		EnableDatagrams:    true,
		MaxIdleTimeout:     30 * time.Second,
		KeepAlivePeriod:    5 * time.Second,
		HandshakeIdleTimeout: 10 * time.Second,
	}
}

// ---------- Server ----------

func runServer(addr string) {
	ln, err := quic.ListenAddr(addr, generateTLSConfig(true), quicConfig())
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("QUIC spike server listening on %s", addr)
	for {
		conn, err := ln.Accept(context.Background())
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		log.Printf("accepted from %s", conn.RemoteAddr())
		go handleServerConn(conn)
	}
}

func handleServerConn(conn *quic.Conn) {
	var rxBytes, rxPackets uint64
	tick := time.NewTicker(1 * time.Second)
	defer tick.Stop()

	go func() {
		var prevB, prevP uint64
		for range tick.C {
			b := atomic.LoadUint64(&rxBytes)
			p := atomic.LoadUint64(&rxPackets)
			db := b - prevB
			dp := p - prevP
			prevB, prevP = b, p
			if dp > 0 {
				log.Printf("server: %.1f Mbps  %d pkts/s  total %d MB / %d pkts",
					float64(db*8)/1e6, dp, b/(1024*1024), p)
			}
		}
	}()

	for {
		msg, err := conn.ReceiveDatagram(context.Background())
		if err != nil {
			log.Printf("recv: %v", err)
			return
		}
		atomic.AddUint64(&rxBytes, uint64(len(msg)))
		atomic.AddUint64(&rxPackets, 1)

		// Echo back (for RTT probes). For pure throughput we'd skip.
		if len(msg) > 0 && msg[0] == 0xFF {
			// 0xFF prefix = please echo
			_ = conn.SendDatagram(msg)
		}
	}
}

// ---------- Client (throughput) ----------

func runClient(addr string, duration time.Duration, packetSize int) {
	conn, err := quic.DialAddr(context.Background(), addr, generateTLSConfig(false), quicConfig())
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("connected to %s, sending %dB datagrams for %v", addr, packetSize, duration)
	log.Printf("max datagram size: %d", conn.ConnectionState().Version)

	pkt := make([]byte, packetSize)
	mrand.Read(pkt)

	var txBytes, txPackets uint64
	var dropped uint64
	end := time.Now().Add(duration)

	tick := time.NewTicker(1 * time.Second)
	defer tick.Stop()
	go func() {
		var prevB, prevP uint64
		for range tick.C {
			b := atomic.LoadUint64(&txBytes)
			p := atomic.LoadUint64(&txPackets)
			d := atomic.LoadUint64(&dropped)
			db := b - prevB
			dp := p - prevP
			prevB, prevP = b, p
			log.Printf("client: %.1f Mbps  %d pkts/s  dropped %d  total %d MB",
				float64(db*8)/1e6, dp, d, b/(1024*1024))
		}
	}()

	for time.Now().Before(end) {
		err := conn.SendDatagram(pkt)
		if err != nil {
			// Datagram queue full — back off briefly
			atomic.AddUint64(&dropped, 1)
			time.Sleep(50 * time.Microsecond)
			continue
		}
		atomic.AddUint64(&txBytes, uint64(len(pkt)))
		atomic.AddUint64(&txPackets, 1)
	}

	log.Printf("DONE: tx=%d MB  pkts=%d  dropped=%d", txBytes/(1024*1024), txPackets, dropped)
	avgMbps := float64(txBytes*8) / duration.Seconds() / 1e6
	log.Printf("AVG throughput: %.1f Mbps", avgMbps)

	_ = conn.CloseWithError(0, "done")
}

// ---------- RTT probe (echo round-trip latency) ----------

func runRTTProbe(addr string, duration time.Duration, interval time.Duration) {
	conn, err := quic.DialAddr(context.Background(), addr, generateTLSConfig(false), quicConfig())
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("RTT probing %s every %v for %v (echo via 0xFF prefix)", addr, interval, duration)

	pkt := make([]byte, 64)
	pkt[0] = 0xFF // server echoes packets starting with 0xFF
	rtts := make([]float64, 0, 4096)

	end := time.Now().Add(duration)
	for time.Now().Before(end) {
		t0 := time.Now()
		if err := conn.SendDatagram(pkt); err != nil {
			log.Printf("send: %v", err)
			time.Sleep(interval)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := conn.ReceiveDatagram(ctx)
		cancel()
		if err != nil {
			log.Printf("recv: %v", err)
			continue
		}
		rtts = append(rtts, float64(time.Since(t0))/float64(time.Millisecond))
		time.Sleep(interval)
	}

	if len(rtts) == 0 {
		log.Fatalf("no successful round-trips")
	}
	sort.Float64s(rtts)
	n := len(rtts)
	fmt.Printf("\n=== RTT STATS (%d probes) ===\n", n)
	fmt.Printf("  min   = %.2f ms\n", rtts[0])
	fmt.Printf("  p50   = %.2f ms\n", rtts[n/2])
	fmt.Printf("  p90   = %.2f ms\n", rtts[int(float64(n)*0.90)])
	fmt.Printf("  p95   = %.2f ms\n", rtts[int(float64(n)*0.95)])
	fmt.Printf("  p99   = %.2f ms\n", rtts[int(float64(n)*0.99)])
	fmt.Printf("  max   = %.2f ms\n", rtts[n-1])

	_ = conn.CloseWithError(0, "done")
}

// ---------- Migration probe ----------
//
// Long-lived client that:
//   - Opens a QUIC connection
//   - Sends one 64-byte datagram/sec, expects echo back (server echoes 0xFF prefixed)
//   - Logs every send/recv/error with wall-clock timestamps
//   - Tracks the local UDP socket address (so we can see if our IP changed)
//   - On error, attempts to keep waiting (does NOT auto-reconnect: we want to know
//     whether QUIC migration kept the connection alive without our intervention)
//
// Designed for the test: start probe with laptop on WiFi, then switch to cellular.
// If QUIC migration works, no error is logged; just brief stalls.
// If migration fails, we see explicit errors and timeout.

func runMigrationProbe(addr string, duration time.Duration) {
	t0 := time.Now()
	// Write to stderr (unbuffered) and flush stdout, so logs appear in real time
	// even when output is redirected to a file under nohup.
	logf := func(format string, args ...interface{}) {
		fmt.Fprintf(os.Stderr, "[%6.2fs] ", time.Since(t0).Seconds())
		fmt.Fprintf(os.Stderr, format, args...)
		fmt.Fprintln(os.Stderr)
	}

	logf("MIGRATION PROBE starting → %s for %v", addr, duration)
	logf("Send 1 datagram/sec, log every event. Switch network mid-stream to test migration.")

	dialCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(dialCtx, addr, generateTLSConfig(false), quicConfig())
	if err != nil {
		logf("DIAL FAILED: %v", err)
		os.Exit(1)
	}
	logf("CONNECTED. local=%s remote=%s", conn.LocalAddr(), conn.RemoteAddr())

	// Receiver goroutine
	type rxEvent struct {
		seq uint32
		t   time.Time
		err error
	}
	rxCh := make(chan rxEvent, 64)
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			msg, err := conn.ReceiveDatagram(ctx)
			cancel()
			now := time.Now()
			if err != nil {
				rxCh <- rxEvent{err: err, t: now}
				return
			}
			if len(msg) >= 5 && msg[0] == 0xFF {
				seq := uint32(msg[1])<<24 | uint32(msg[2])<<16 | uint32(msg[3])<<8 | uint32(msg[4])
				rxCh <- rxEvent{seq: seq, t: now}
			}
		}
	}()

	pkt := make([]byte, 64)
	pkt[0] = 0xFF
	var seq uint32
	pending := make(map[uint32]time.Time) // seq -> sent_at
	var lastLocal string = conn.LocalAddr().String()

	tick := time.NewTicker(1 * time.Second)
	defer tick.Stop()
	end := time.After(duration)

	var sentCount, rxCount, errCount uint64

mainloop:
	for {
		select {
		case <-end:
			logf("DURATION DONE — sent=%d rx=%d err=%d", sentCount, rxCount, errCount)
			break mainloop

		case <-tick.C:
			// Check if local addr changed
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
			err := conn.SendDatagram(pkt)
			if err != nil {
				logf("SEND seq=%d FAILED: %v", seq, err)
				errCount++
				continue
			}
			pending[seq] = time.Now()
			sentCount++

			// Reap stale pending (>10s with no echo)
			for s, sent := range pending {
				if time.Since(sent) > 10*time.Second {
					logf("LOST seq=%d (no echo after %.1fs)", s, time.Since(sent).Seconds())
					delete(pending, s)
				}
			}

		case ev := <-rxCh:
			if ev.err != nil {
				logf("RECV ERROR: %v (this typically means the QUIC connection died)", ev.err)
				errCount++
				// Continue and see if more errors come or if QUIC recovers
				continue
			}
			sent, ok := pending[ev.seq]
			if ok {
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

	_ = conn.CloseWithError(0, "done")
}

