// tcp-rtt-probe: opens a TCP connection to a (host, port) every <cadence>
// for <duration>, records the time from first SYN to either connect-success
// or RST (closed port). Emits p50/p95/p99/max + raw samples.
//
// Unlike ICMP ping, this measures the same code path real TCP traffic takes
// through the VPN tunnel — it's a better proxy for interactive-packet
// latency than ping when the tunnel is under load.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"sort"
	"syscall"
	"time"
)

func main() {
	target := flag.String("target", "", "<host>:<port> to probe (port may be closed; we measure SYN->RST time)")
	cadence := flag.Duration("cadence", 50*time.Millisecond, "inter-probe delay")
	duration := flag.Duration("duration", 60*time.Second, "total probe duration")
	timeout := flag.Duration("timeout", 2*time.Second, "per-probe timeout")
	flag.Parse()

	if *target == "" {
		fmt.Fprintln(os.Stderr, "need -target host:port")
		os.Exit(2)
	}

	samples := make([]time.Duration, 0, int(*duration / *cadence))
	timeouts := 0
	otherErrs := 0

	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	tick := time.NewTicker(*cadence)
	defer tick.Stop()

	dialer := net.Dialer{Timeout: *timeout}
	start := time.Now()

	for {
		select {
		case <-ctx.Done():
			report(samples, timeouts, otherErrs, time.Since(start))
			return
		case <-tick.C:
			t0 := time.Now()
			conn, err := dialer.DialContext(ctx, "tcp", *target)
			dt := time.Since(t0)
			if conn != nil {
				conn.Close()
				samples = append(samples, dt)
				continue
			}
			// err != nil
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				timeouts++
				continue
			}
			// "connection refused" = RST = measured RTT successfully
			if isConnRefused(err) {
				samples = append(samples, dt)
				continue
			}
			otherErrs++
		}
	}
}

func isConnRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}

func report(samples []time.Duration, timeouts, otherErrs int, wall time.Duration) {
	fmt.Printf("TCP-RTT probe — %d samples in %v (timeouts=%d, other-errs=%d)\n",
		len(samples), wall.Round(time.Millisecond), timeouts, otherErrs)
	if len(samples) == 0 {
		fmt.Println("no successful samples")
		return
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })

	p := func(q float64) time.Duration {
		idx := int(float64(len(samples)-1) * q)
		if idx >= len(samples) {
			idx = len(samples) - 1
		}
		return samples[idx]
	}

	var sum time.Duration
	for _, s := range samples {
		sum += s
	}
	mean := sum / time.Duration(len(samples))

	fmt.Printf("p50   = %v\n", p(0.50).Round(100*time.Microsecond))
	fmt.Printf("p90   = %v\n", p(0.90).Round(100*time.Microsecond))
	fmt.Printf("p95   = %v\n", p(0.95).Round(100*time.Microsecond))
	fmt.Printf("p99   = %v\n", p(0.99).Round(100*time.Microsecond))
	fmt.Printf("max   = %v\n", samples[len(samples)-1].Round(100*time.Microsecond))
	fmt.Printf("mean  = %v\n", mean.Round(100*time.Microsecond))
	fmt.Printf("min   = %v\n", samples[0].Round(100*time.Microsecond))
	fmt.Printf("\n# raw samples (ms), one per line:\n")
	for _, s := range samples {
		fmt.Printf("%.3f\n", float64(s.Microseconds())/1000.0)
	}
}
