// `hop-agent migration` — runs the QUIC migration probe against the control
// plane's QUIC endpoint. Used to validate that a QUIC connection survives a
// local IP change (e.g. WiFi → cellular handoff).
//
// This is the foundation of Phase 1 of the QUIC transport migration work
// (see plan/purring-chasing-babbage). Today it only proves that quic-go's
// connection migration handles the network changes our users hit. Real mesh
// traffic over QUIC is the next step once this validates.
//
// Usage:
//   hop-agent migration --addr <quic-host:port> [--duration 5m] [--interval 1s]
//
// Output goes to stderr (unbuffered, so it's captured live under nohup).

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/trustos/hopssh/internal/quictransport"
)

func runMigration(args []string) {
	fs := flag.NewFlagSet("migration", flag.ExitOnError)
	addr := fs.String("addr", "", "QUIC endpoint (host:port). Required.")
	duration := fs.Duration("duration", 5*time.Minute, "How long to run the probe.")
	interval := fs.Duration("interval", time.Second, "Delay between probe datagrams.")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: hop-agent migration --addr <host:port> [--duration 5m] [--interval 1s]\n\n")
		fmt.Fprintln(os.Stderr, "Long-lived QUIC connection that sends one datagram per interval and expects an echo.")
		fmt.Fprintln(os.Stderr, "While running, switch the local network (e.g. WiFi → cellular). Logs every event.")
		fmt.Fprintln(os.Stderr, "If QUIC migration works, the connection survives the network switch.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *addr == "" {
		fs.Usage()
		os.Exit(2)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancel cleanly on Ctrl-C.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	cfg := quictransport.ProbeConfig{
		Addr:     *addr,
		Duration: *duration,
		Interval: *interval,
		Out:      os.Stderr,
	}
	if err := quictransport.RunProbe(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "probe error: %v\n", err)
		os.Exit(1)
	}
}
