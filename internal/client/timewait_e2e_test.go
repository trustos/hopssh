package client

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// TestE2E_ConnectAgainAfterTimeWait: the side of a connection that closes
// first keeps it in TIME_WAIT for 60 s. Nebula's userspace service accepts
// through gVisor's forwarder, not a listening socket, and gVisor let a new SYN
// reopen a connection in TIME_WAIT only when a listening socket was there.
// Otherwise the SYN and every retry were dropped until the 60 s were over.
// The server's proxy opens a connection per request from a random port to an
// agent that closes first, so in a run of requests to a userspace agent one in
// a few hundred hung for 30 s (TestE2E_Proxy, 2026-11-27). Patch
// gvisor-time-wait hands such a SYN to the forwarder.
func TestE2E_ConnectAgainAfterTimeWait(t *testing.T) {
	if raceEnabled {
		// Measured 2026-11-27 on the Mac: under -race a connection through
		// Nebula takes about 0.5 s, so 2000 would take 17 min and the 2 s bar
		// says little. TestTimeWaitReopen runs the patched path under -race.
		t.Skip("too slow under -race; TestTimeWaitReopen covers the race run")
	}
	m := newE2EMesh(t)
	portA, portB := freeUDPPort(t), freeUDPPort(t)
	ipA, ipB := e2eSubnet+"6", e2eSubnet+"7"
	m.enroll("a", ipA, portA, time.Hour, map[string][]string{ipB: {localUDP(portB)}})
	m.enroll("b", ipB, portB, time.Hour, map[string][]string{ipA: {localUDP(portA)}})
	if err := m.c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	a, b := m.c.instances.get("a"), m.c.instances.get("b")
	d, ok := a.currentSvc().(meshDialer)
	if !ok {
		t.Fatal("a has no userspace service")
	}
	l, err := b.currentSvc().Listen("tcp", ":7000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close() // b closes first: the TIME_WAIT is b's
				c.SetDeadline(time.Now().Add(5 * time.Second))
				buf := make([]byte, 1)
				if _, err := io.ReadFull(c, buf); err == nil {
					c.Write(buf)
				}
			}()
		}
	}()

	target := net.JoinHostPort(ipB, "7000")
	connect := func(timeout time.Duration) error {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		c, err := d.DialContext(ctx, "tcp", target)
		if err != nil {
			return err
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(timeout))
		if _, err := c.Write([]byte{1}); err != nil {
			return err
		}
		if _, err := io.ReadFull(c, make([]byte, 1)); err != nil {
			return err
		}
		_, err = io.Copy(io.Discard, c) // until b's FIN
		return err
	}
	within(t, 20*time.Second, "a's first connection to b", func() bool { return connect(2*time.Second) == nil })

	// a picks each port at random among 49536 (16000 to 65535, gVisor's
	// PickEphemeralPort). Connection n meets one of b's TIME_WAITs (those of
	// the last 60 s) with a chance of (n-1)/49536, so after about 280 on
	// average; that none of 2000 does has a chance of about e^-40, or e^-30
	// when the loop takes two minutes. Without the patch such a SYN and its
	// resend after 1 s are dropped, and the 2 s are over. A SYN that is lost
	// once and resent is no failure here: TestTimeWaitReopen pins that the
	// first SYN is answered.
	for i := range 2000 {
		t0 := time.Now()
		err := connect(2 * time.Second)
		if took := time.Since(t0); err != nil {
			t.Fatalf("connection %d to b, which closed every one before first: %v after %v", i+2, err, took.Round(time.Millisecond))
		}
	}
}
