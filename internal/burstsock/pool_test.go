package burstsock

import (
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestPool_OpensCount(t *testing.T) {
	p, err := NewPool(8)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()
	if p.Size() != 8 {
		t.Errorf("Size = %d, want 8", p.Size())
	}
	ports := p.LocalPorts()
	if len(ports) != 8 {
		t.Errorf("LocalPorts len = %d, want 8", len(ports))
	}
	// Every port must be unique (kernel won't double-bind).
	seen := map[uint16]bool{}
	for _, port := range ports {
		if port == 0 {
			t.Errorf("port=0 not yet bound?")
		}
		if seen[port] {
			t.Errorf("duplicate port %d", port)
		}
		seen[port] = true
	}
}

func TestPool_DefaultSize(t *testing.T) {
	p, err := NewPool(0)
	if err != nil {
		t.Skipf("default-size pool open failed (likely fd limit): %v", err)
	}
	defer p.Close()
	if p.Size() != DefaultSocketCount {
		t.Errorf("default Size = %d, want %d", p.Size(), DefaultSocketCount)
	}
}

func TestPool_CloseIdempotent(t *testing.T) {
	p, _ := NewPool(2)
	p.Close()
	p.Close() // must not panic
}

func TestPool_BurstAndReceive(t *testing.T) {
	// Run a tiny echo server on loopback. The pool sends one packet
	// to it, the server echoes back, and Receive() should see the
	// reply on whichever socket the kernel routed it to.
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := server.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = server.WriteToUDP(buf[:n], from)
		}
	}()
	srvAddr := server.LocalAddr().(*net.UDPAddr)
	target := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(srvAddr.Port))

	p, err := NewPool(4)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	go func() {
		// Pacing=0 — small burst, no rate-limit concern on loopback.
		p.Burst([]netip.AddrPort{target}, []byte("ping"), 0)
	}()

	from, idx, err := p.Receive(2 * time.Second)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if from.Addr().String() != "127.0.0.1" {
		t.Errorf("from addr = %s", from.Addr())
	}
	if idx < 0 || idx >= 4 {
		t.Errorf("idx out of range: %d", idx)
	}
}

func TestPool_ReceiveTimesOut(t *testing.T) {
	p, _ := NewPool(2)
	defer p.Close()
	_, _, err := p.Receive(150 * time.Millisecond)
	if err == nil {
		t.Error("expected timeout error")
	}
}

func TestPool_BurstOnClosedNoOp(t *testing.T) {
	p, _ := NewPool(2)
	p.Close()
	target := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), 65000)
	sent, _ := p.Burst([]netip.AddrPort{target}, []byte("x"), 0)
	if sent != 0 {
		t.Errorf("closed pool sent %d packets, want 0", sent)
	}
}
