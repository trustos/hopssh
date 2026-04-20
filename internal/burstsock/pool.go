package burstsock

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"
)

// DefaultSocketCount is sqrt(65536) — the birthday-paradox sweet
// spot. Override via NewPool's count parameter for testing.
const DefaultSocketCount = 256

// Pool is a set of N UDP sockets bound to kernel-assigned ephemeral
// ports, used to spray simultaneous packets across many source ports.
// Each socket is its own outbound flow as far as the local NAT is
// concerned, multiplying the chances of a punch hitting a peer's
// random-port symmetric NAT mapping.
type Pool struct {
	conns []*net.UDPConn
	mu    sync.Mutex
	closed bool
}

// NewPool opens count UDP sockets on 0.0.0.0:0. Caller is responsible
// for Close. count=0 falls back to DefaultSocketCount.
func NewPool(count int) (*Pool, error) {
	if count <= 0 {
		count = DefaultSocketCount
	}
	p := &Pool{conns: make([]*net.UDPConn, 0, count)}
	for i := 0; i < count; i++ {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
		if err != nil {
			p.Close()
			return nil, fmt.Errorf("burstsock: open socket %d/%d: %w", i+1, count, err)
		}
		p.conns = append(p.conns, conn)
	}
	return p, nil
}

// Close releases all sockets. Idempotent.
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	for _, c := range p.conns {
		_ = c.Close()
	}
}

// Size returns the number of sockets in the pool.
func (p *Pool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.conns)
}

// LocalPorts returns the kernel-assigned source ports of every socket
// in the pool, in stable index order. Used by the coordinator when
// telling a peer which source ports we're probing FROM (so it can
// add them to its candidate destination list).
func (p *Pool) LocalPorts() []uint16 {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]uint16, 0, len(p.conns))
	for _, c := range p.conns {
		la, ok := c.LocalAddr().(*net.UDPAddr)
		if !ok || la == nil {
			continue
		}
		out = append(out, uint16(la.Port))
	}
	return out
}

// Burst sends payload to every (target_addr × source_socket) pair.
// Returns the number of successful sends and the number of write
// errors. Cap on total packets is len(p.conns) × len(targets) — at
// the default 256 × 256 = 65536 packets, expect ~3-5 seconds at
// typical carrier rate limits.
//
// pacing is the per-socket inter-send delay. 10 ms × 256 packets =
// 2.56 s per source — keeps us under the typical 100 pps carrier
// rate-limit per source IP. pacing=0 means flat-out.
func (p *Pool) Burst(targets []netip.AddrPort, payload []byte, pacing time.Duration) (sent, errs int) {
	if len(targets) == 0 || len(payload) == 0 {
		return 0, 0
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return 0, 0
	}
	conns := append([]*net.UDPConn(nil), p.conns...)
	p.mu.Unlock()

	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, conn := range conns {
		wg.Add(1)
		go func(conn *net.UDPConn) {
			defer wg.Done()
			localSent, localErrs := 0, 0
			for _, target := range targets {
				udpAddr := &net.UDPAddr{
					IP:   target.Addr().AsSlice(),
					Port: int(target.Port()),
				}
				if _, err := conn.WriteToUDP(payload, udpAddr); err != nil {
					localErrs++
					continue
				}
				localSent++
				if pacing > 0 {
					time.Sleep(pacing)
				}
			}
			mu.Lock()
			sent += localSent
			errs += localErrs
			mu.Unlock()
		}(conn)
	}
	wg.Wait()
	return sent, errs
}

// Receive waits up to timeout for the FIRST inbound packet on ANY
// socket in the pool. Returns the source AddrPort of the responder
// and the local socket index that received it. errTimeout if no
// packet arrived. Caller can match the source AddrPort against
// expected peer endpoints to decide if the punch succeeded.
func (p *Pool) Receive(timeout time.Duration) (netip.AddrPort, int, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return netip.AddrPort{}, -1, errors.New("burstsock: pool closed")
	}
	conns := append([]*net.UDPConn(nil), p.conns...)
	p.mu.Unlock()

	type hit struct {
		from netip.AddrPort
		idx  int
	}
	hitCh := make(chan hit, 1)
	deadline := time.Now().Add(timeout)
	for i, conn := range conns {
		go func(idx int, conn *net.UDPConn) {
			_ = conn.SetReadDeadline(deadline)
			buf := make([]byte, 1500)
			_, addr, err := conn.ReadFromUDP(buf)
			if err != nil || addr == nil {
				return
			}
			ap, ok := netip.AddrFromSlice(addr.IP)
			if !ok {
				return
			}
			select {
			case hitCh <- hit{netip.AddrPortFrom(ap, uint16(addr.Port)), idx}:
			default:
			}
		}(i, conn)
	}

	select {
	case h := <-hitCh:
		return h.from, h.idx, nil
	case <-time.After(timeout):
		return netip.AddrPort{}, -1, errTimeout
	}
}

var errTimeout = errors.New("burstsock: receive timed out")
