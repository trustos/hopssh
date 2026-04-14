package udp

import (
	"encoding/binary"
	"net/netip"
	"sync"
	"time"

	"github.com/slackhq/nebula/config"
)

const (
	coalesceLenPrefix     = 2
	coalesceMaxDatagram   = 1440
	coalesceFlushInterval = 100 * time.Microsecond
	nebulaVersionNibble   = 1 << 4 // Nebula v1 header: upper nibble = 0x1
)

// CoalescingConn wraps a Conn and batches WriteTo calls to the same
// destination into a single UDP datagram with length-prefix framing.
type CoalescingConn struct {
	inner Conn

	mu     sync.Mutex
	peers  map[netip.AddrPort]*peerBuf
	closed bool
}

type peerBuf struct {
	buf    [coalesceMaxDatagram]byte
	offset int
	addr   netip.AddrPort
	conn   *CoalescingConn
	timer  *time.Timer
}

// NewCoalescingConn wraps a Conn with packet coalescing.
func NewCoalescingConn(inner Conn) *CoalescingConn {
	return &CoalescingConn{
		inner: inner,
		peers: make(map[netip.AddrPort]*peerBuf),
	}
}

func (c *CoalescingConn) WriteTo(b []byte, addr netip.AddrPort) error {
	needed := coalesceLenPrefix + len(b)

	if needed > coalesceMaxDatagram {
		return c.inner.WriteTo(b, addr)
	}

	c.mu.Lock()
	pb, ok := c.peers[addr]
	if !ok {
		pb = &peerBuf{addr: addr, conn: c}
		pb.timer = time.AfterFunc(coalesceFlushInterval, func() {
			c.flushPeer(addr)
		})
		pb.timer.Stop()
		c.peers[addr] = pb
	}

	if pb.offset+needed > coalesceMaxDatagram {
		c.flushPeerLocked(pb)
	}

	binary.BigEndian.PutUint16(pb.buf[pb.offset:], uint16(len(b)))
	copy(pb.buf[pb.offset+coalesceLenPrefix:], b)
	pb.offset += needed

	if pb.offset == needed {
		pb.timer.Reset(coalesceFlushInterval)
	}
	c.mu.Unlock()
	return nil
}

func (c *CoalescingConn) flushPeer(addr netip.AddrPort) {
	c.mu.Lock()
	pb, ok := c.peers[addr]
	if ok && pb.offset > 0 {
		c.flushPeerLocked(pb)
	}
	c.mu.Unlock()
}

func (c *CoalescingConn) flushPeerLocked(pb *peerBuf) {
	if pb.offset == 0 {
		return
	}
	pb.timer.Stop()
	out := make([]byte, pb.offset)
	copy(out, pb.buf[:pb.offset])
	pb.offset = 0
	c.inner.WriteTo(out, pb.addr)
}

// ListenOut wraps the inner ListenOut to decoalesce received datagrams.
func (c *CoalescingConn) ListenOut(r EncReader) {
	c.inner.ListenOut(func(addr netip.AddrPort, payload []byte) {
		for _, pkt := range SplitCoalesced(payload) {
			r(addr, pkt)
		}
	})
}

func (c *CoalescingConn) Rebind() error                      { return c.inner.Rebind() }
func (c *CoalescingConn) LocalAddr() (netip.AddrPort, error) { return c.inner.LocalAddr() }
func (c *CoalescingConn) ReloadConfig(cfg *config.C)         { c.inner.ReloadConfig(cfg) }
func (c *CoalescingConn) SupportsMultipleReaders() bool      { return c.inner.SupportsMultipleReaders() }

func (c *CoalescingConn) Close() error {
	c.mu.Lock()
	c.closed = true
	for _, pb := range c.peers {
		pb.timer.Stop()
	}
	c.mu.Unlock()
	return c.inner.Close()
}

// SplitCoalesced separates a coalesced datagram into individual packets.
// If the datagram is a single Nebula packet (not coalesced), returns it as-is.
func SplitCoalesced(data []byte) [][]byte {
	if len(data) < 4 || data[0]&0xF0 == nebulaVersionNibble {
		return [][]byte{data}
	}

	var packets [][]byte
	offset := 0
	for offset+coalesceLenPrefix <= len(data) {
		pktLen := int(binary.BigEndian.Uint16(data[offset:]))
		offset += coalesceLenPrefix
		if pktLen == 0 || offset+pktLen > len(data) {
			break
		}
		packets = append(packets, data[offset:offset+pktLen])
		offset += pktLen
	}
	if len(packets) == 0 {
		return [][]byte{data}
	}
	return packets
}
