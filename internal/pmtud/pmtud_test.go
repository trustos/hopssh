package pmtud

import (
	"net/netip"
	"testing"
)

var testPeer = netip.MustParseAddr("10.42.1.6")

func newTestProber() (*Prober, *[]int, *[]int) {
	var probes []int
	var mtus []int
	p := New(
		func(peer netip.Addr, size int) { probes = append(probes, size) },
		func(mtu int) error { mtus = append(mtus, mtu); return nil },
	)
	return p, &probes, &mtus
}

func TestBinarySearchConverges(t *testing.T) {
	p, probes, mtus := newTestProber()
	p.AddPeer(testPeer)

	// Simulate: path supports up to 2800 bytes.
	pathMTU := 2800

	for i := 0; i < 20; i++ {
		p.ProbeAll()
		if len(*probes) == 0 {
			break
		}
		lastProbe := (*probes)[len(*probes)-1]
		if lastProbe <= pathMTU {
			p.HandleReply(testPeer, lastProbe)
		} else {
			p.HandleTimeout(testPeer)
		}
	}

	got := p.GetPeerMTU(testPeer)
	if got < pathMTU-StepSize || got > pathMTU {
		t.Fatalf("expected MTU near %d, got %d", pathMTU, got)
	}

	if len(*mtus) == 0 {
		t.Fatal("expected at least one MTU change")
	}
	finalMTU := (*mtus)[len(*mtus)-1]
	if finalMTU < pathMTU-StepSize || finalMTU > pathMTU {
		t.Fatalf("expected final MTU near %d, got %d", pathMTU, finalMTU)
	}

	t.Logf("Converged to MTU %d in %d probes", got, len(*probes))
}

func TestBinarySearchLowMTU(t *testing.T) {
	p, probes, _ := newTestProber()
	p.AddPeer(testPeer)

	// Path only supports base MTU — all probes above 1440 fail.
	for i := 0; i < 20; i++ {
		p.ProbeAll()
		if len(*probes) == 0 {
			break
		}
		lastProbe := (*probes)[len(*probes)-1]
		if lastProbe <= BaseMTU {
			p.HandleReply(testPeer, lastProbe)
		} else {
			p.HandleTimeout(testPeer)
		}
	}

	got := p.GetPeerMTU(testPeer)
	if got != BaseMTU {
		t.Fatalf("expected MTU %d (base), got %d", BaseMTU, got)
	}
}

func TestBinarySearchHighMTU(t *testing.T) {
	p, probes, _ := newTestProber()
	p.AddPeer(testPeer)

	// Path supports everything — jumbo frames.
	for i := 0; i < 20; i++ {
		p.ProbeAll()
		if len(*probes) == 0 {
			break
		}
		p.HandleReply(testPeer, (*probes)[len(*probes)-1])
	}

	got := p.GetPeerMTU(testPeer)
	if got < MaxMTU-StepSize {
		t.Fatalf("expected MTU near %d, got %d", MaxMTU, got)
	}
	t.Logf("Converged to MTU %d", got)
}

func TestMultiPeerMinMTU(t *testing.T) {
	p, probes, mtus := newTestProber()

	peer1 := netip.MustParseAddr("10.42.1.2")
	peer2 := netip.MustParseAddr("10.42.1.3")
	p.AddPeer(peer1)
	p.AddPeer(peer2)

	// Peer1 supports 4400, peer2 supports 1440.
	pathMTUs := map[netip.Addr]int{
		peer1: 4400,
		peer2: 1440,
	}

	for i := 0; i < 30; i++ {
		p.ProbeAll()
		if len(*probes) == 0 {
			break
		}
		// Handle all probes by checking which peer was probed.
		// Since probes don't carry peer info here, simulate per-peer.
		p.mu.Lock()
		for addr, ps := range p.peers {
			if ps.Probing {
				if ps.ProbeSize <= pathMTUs[addr] {
					p.mu.Unlock()
					p.HandleReply(addr, ps.ProbeSize)
					p.mu.Lock()
				} else {
					p.mu.Unlock()
					p.HandleTimeout(addr)
					p.mu.Lock()
				}
			}
		}
		p.mu.Unlock()
		*probes = nil
	}

	got := p.GetMTU()
	if got != BaseMTU {
		t.Fatalf("expected global MTU %d (min of peers), got %d", BaseMTU, got)
	}

	got1 := p.GetPeerMTU(peer1)
	if got1 < 4400-StepSize {
		t.Fatalf("expected peer1 MTU near 4400, got %d", got1)
	}

	got2 := p.GetPeerMTU(peer2)
	if got2 != BaseMTU {
		t.Fatalf("expected peer2 MTU %d, got %d", BaseMTU, got2)
	}

	_ = mtus
	t.Logf("Global MTU: %d, Peer1: %d, Peer2: %d", got, got1, got2)
}

func TestTimeoutReducesCeiling(t *testing.T) {
	p, _, _ := newTestProber()
	p.AddPeer(testPeer)

	p.ProbeAll()

	p.mu.Lock()
	ps := p.peers[testPeer]
	probeSize := ps.ProbeSize
	p.mu.Unlock()

	p.HandleTimeout(testPeer)

	p.mu.Lock()
	defer p.mu.Unlock()
	if ps.Ceiling != probeSize {
		t.Fatalf("expected ceiling to drop to %d, got %d", probeSize, ps.Ceiling)
	}
}

func TestReplyRaisesFloor(t *testing.T) {
	p, _, _ := newTestProber()
	p.AddPeer(testPeer)

	p.ProbeAll()

	p.mu.Lock()
	ps := p.peers[testPeer]
	probeSize := ps.ProbeSize
	p.mu.Unlock()

	p.HandleReply(testPeer, probeSize)

	p.mu.Lock()
	defer p.mu.Unlock()
	if ps.Floor != probeSize {
		t.Fatalf("expected floor to rise to %d, got %d", probeSize, ps.Floor)
	}
}

func TestNoPeerNoPanic(t *testing.T) {
	p, _, _ := newTestProber()
	p.HandleReply(testPeer, 5000)
	p.HandleTimeout(testPeer)
	p.ProbeAll()
}

func TestConvergesWithinMaxProbes(t *testing.T) {
	p, _, _ := newTestProber()
	p.AddPeer(testPeer)

	pathMTU := 2800
	totalProbes := 0

	for i := 0; i < 20; i++ {
		p.ProbeAll()

		p.mu.Lock()
		ps := p.peers[testPeer]
		if !ps.Probing {
			p.mu.Unlock()
			break
		}
		probeSize := ps.ProbeSize
		p.mu.Unlock()

		totalProbes++
		if probeSize <= pathMTU {
			p.HandleReply(testPeer, probeSize)
		} else {
			p.HandleTimeout(testPeer)
		}
	}

	if totalProbes > 10 {
		t.Fatalf("expected convergence in ≤10 probes, took %d", totalProbes)
	}
	t.Logf("Converged in %d probes", totalProbes)
}
