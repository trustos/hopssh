// portmap-test is a standalone CLI that exercises the internal/portmap
// Manager against a real gateway. Intended for integration testing and
// manual verification:
//
//	go run ./cmd/portmap-test --port 44244
//
// Hold for a few seconds, then Ctrl-C. The mapping is unmapped cleanly
// on exit. Verify externally via `upnpc -l` or by trying to reach
// <public-ip>:<port> from an off-network client.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/trustos/hopssh/internal/portmap"
)

func main() {
	port := flag.Uint("port", 4242, "internal UDP port to map")
	verbose := flag.Bool("v", false, "verbose (debug-level) logs")
	flag.Parse()

	l := logrus.New()
	l.SetLevel(logrus.InfoLevel)
	if *verbose {
		l.SetLevel(logrus.DebugLevel)
	}

	gw, err := portmap.DiscoverGateway()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gateway: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("gateway: %s\n", gw)

	m := portmap.New(l, uint16(*port))
	m.OnChange(func(old, cur netip.AddrPort) {
		if !old.IsValid() {
			fmt.Printf("public: %s\n", cur)
			return
		}
		fmt.Printf("public changed: %s -> %s\n", old, cur)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "start: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("probing (up to 3 s)... press Ctrl-C to unmap and exit")

	// Poll briefly for the mapping to land.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m.Current().IsValid() {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !m.Current().IsValid() {
		fmt.Println("no mapping established (no protocol answered); exiting")
		m.Stop()
		os.Exit(2)
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	<-sigs

	fmt.Println("\nunmapping...")
	m.Stop()
	fmt.Println("done")
}
