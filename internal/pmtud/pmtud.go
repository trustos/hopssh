package pmtud

import (
	"context"
	"log"
	"net/netip"
	"sync"
	"time"
)

const (
	BaseMTU       = 1440
	MaxMTU        = 9000
	ProbeInterval = 5 * time.Minute
	ProbeTimeout  = 2 * time.Second
	StepSize      = 20 // stop binary search when range is this narrow
)

// SendProbeFunc sends a Nebula TestRequest with the given payload size to a peer.
type SendProbeFunc func(peer netip.Addr, payloadSize int)

// SetMTUFunc changes the TUN interface MTU.
type SetMTUFunc func(mtu int) error

// PeerState tracks the binary search state for one peer's path MTU.
type PeerState struct {
	Floor     int
	Ceiling   int
	Current   int
	ProbeSize int
	ProbeSent time.Time
	Probing   bool
}

// Prober discovers the optimal MTU for each peer via DPLPMTUD (RFC 8899).
type Prober struct {
	mu          sync.Mutex
	peers       map[netip.Addr]*PeerState
	sendProbe   SendProbeFunc
	setMTU      SetMTUFunc
	currentMTU  int
	initialProbe bool
}

// New creates a Prober with the given callbacks.
func New(sendProbe SendProbeFunc, setMTU SetMTUFunc) *Prober {
	return &Prober{
		peers:      make(map[netip.Addr]*PeerState),
		sendProbe:  sendProbe,
		setMTU:     setMTU,
		currentMTU: BaseMTU,
	}
}

// AddPeer registers a peer for PMTUD probing.
func (p *Prober) AddPeer(addr netip.Addr) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.peers[addr]; !ok {
		p.peers[addr] = &PeerState{
			Floor:   BaseMTU,
			Ceiling: MaxMTU,
			Current: BaseMTU,
		}
	}
}

// HandleReply is called when a TestReply is received from a peer.
// The size is the payload length of the reply.
func (p *Prober) HandleReply(peer netip.Addr, size int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	ps, ok := p.peers[peer]
	if !ok || !ps.Probing {
		return
	}

	if size >= ps.ProbeSize {
		ps.Floor = ps.ProbeSize
		ps.Probing = false

		if ps.Floor > ps.Current {
			ps.Current = ps.Floor
			p.recalcMTU()
		}
	}
}

// HandleTimeout is called when a probe to a peer times out.
func (p *Prober) HandleTimeout(peer netip.Addr) {
	p.mu.Lock()
	defer p.mu.Unlock()

	ps, ok := p.peers[peer]
	if !ok || !ps.Probing {
		return
	}

	ps.Ceiling = ps.ProbeSize
	ps.Probing = false

	if ps.Ceiling < ps.Current {
		ps.Current = ps.Ceiling
		p.recalcMTU()
	}
}

// ProbeAll sends one probe per peer that needs probing.
func (p *Prober) ProbeAll() {
	p.mu.Lock()
	var toProbe []struct {
		addr netip.Addr
		size int
	}

	for addr, ps := range p.peers {
		if ps.Probing {
			if time.Since(ps.ProbeSent) > ProbeTimeout {
				ps.Ceiling = ps.ProbeSize
				ps.Probing = false
				if ps.Ceiling < ps.Current {
					ps.Current = ps.Ceiling
				}
			}
			continue
		}

		if ps.Ceiling-ps.Floor <= StepSize {
			continue
		}

		mid := (ps.Floor + ps.Ceiling) / 2
		ps.ProbeSize = mid
		ps.ProbeSent = time.Now()
		ps.Probing = true

		toProbe = append(toProbe, struct {
			addr netip.Addr
			size int
		}{addr, mid})
	}
	p.mu.Unlock()

	for _, tp := range toProbe {
		p.sendProbe(tp.addr, tp.size)
	}
}

// recalcMTU sets TUN MTU to min(all peer discovered MTUs). Must hold p.mu.
func (p *Prober) recalcMTU() {
	minMTU := MaxMTU
	for _, ps := range p.peers {
		if ps.Current < minMTU {
			minMTU = ps.Current
		}
	}
	if minMTU < BaseMTU {
		minMTU = BaseMTU
	}

	if minMTU != p.currentMTU {
		old := p.currentMTU
		p.currentMTU = minMTU
		if err := p.setMTU(minMTU); err != nil {
			log.Printf("[pmtud] failed to set MTU %d: %v", minMTU, err)
		} else {
			log.Printf("[pmtud] MTU changed: %d → %d", old, minMTU)
		}
	}
}

// GetMTU returns the current discovered MTU.
func (p *Prober) GetMTU() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.currentMTU
}

// GetPeerMTU returns the discovered MTU for a specific peer.
func (p *Prober) GetPeerMTU(addr netip.Addr) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ps, ok := p.peers[addr]; ok {
		return ps.Current
	}
	return BaseMTU
}

// Run starts the background probing loop. Blocks until ctx is cancelled.
func (p *Prober) Run(ctx context.Context) {
	// Initial rapid probing: probe every 2s until converged.
	initialTicker := time.NewTicker(ProbeTimeout)
	defer initialTicker.Stop()

	for i := 0; i < 10; i++ {
		select {
		case <-ctx.Done():
			return
		case <-initialTicker.C:
			p.ProbeAll()
			if p.allConverged() {
				log.Printf("[pmtud] initial discovery complete: MTU %d", p.GetMTU())
				goto periodic
			}
		}
	}
	log.Printf("[pmtud] initial discovery done (max probes): MTU %d", p.GetMTU())

periodic:
	initialTicker.Stop()
	ticker := time.NewTicker(ProbeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.resetSearch()
			for i := 0; i < 10; i++ {
				time.Sleep(ProbeTimeout)
				p.ProbeAll()
				if p.allConverged() {
					break
				}
			}
			log.Printf("[pmtud] re-probe complete: MTU %d", p.GetMTU())
		}
	}
}

func (p *Prober) allConverged() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ps := range p.peers {
		if ps.Ceiling-ps.Floor > StepSize {
			return false
		}
	}
	return true
}

func (p *Prober) resetSearch() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ps := range p.peers {
		ps.Floor = BaseMTU
		ps.Ceiling = MaxMTU
		ps.Probing = false
	}
}
