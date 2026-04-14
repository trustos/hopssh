package coalesce

import (
	"encoding/binary"
	"sync"
	"time"
)

const (
	// MaxDatagramSize is the maximum coalesced UDP datagram size.
	// Matches the TUN MTU to avoid IP fragmentation on the outer path.
	MaxDatagramSize = 1440

	// LenPrefixSize is the byte size of the length prefix per inner packet.
	LenPrefixSize = 2

	// DefaultFlushInterval is the maximum time packets sit in the buffer
	// before being flushed. 1ms balances latency vs syscall reduction.
	DefaultFlushInterval = time.Millisecond

	// magicVersion is Nebula's header version nibble (upper 4 bits of byte 0).
	// Used to distinguish single Nebula packets from coalesced datagrams.
	magicVersion = 1 << 4 // 0x10
)

// FlushFunc is called with the coalesced datagram bytes to send.
type FlushFunc func(data []byte)

// Coalescer batches multiple encrypted Nebula packets into a single UDP
// datagram, reducing sendto syscalls. Thread-safe.
type Coalescer struct {
	mu       sync.Mutex
	buf      []byte
	offset   int
	flushFn  FlushFunc
	timer    *time.Timer
	maxSize  int
	interval time.Duration
}

// NewCoalescer creates a coalescer that calls flushFn with batched packets.
func NewCoalescer(flushFn FlushFunc) *Coalescer {
	return NewCoalescerWithConfig(flushFn, MaxDatagramSize, DefaultFlushInterval)
}

// NewCoalescerWithConfig creates a coalescer with custom size and interval.
func NewCoalescerWithConfig(flushFn FlushFunc, maxSize int, interval time.Duration) *Coalescer {
	c := &Coalescer{
		buf:      make([]byte, maxSize),
		flushFn:  flushFn,
		maxSize:  maxSize,
		interval: interval,
	}
	c.timer = time.AfterFunc(interval, c.timerFlush)
	c.timer.Stop()
	return c
}

// Add appends an encrypted packet to the coalescing buffer. If adding the
// packet would exceed the max datagram size, the current buffer is flushed
// first. If the packet alone exceeds the max size, it is sent immediately
// without coalescing.
func (c *Coalescer) Add(packet []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	needed := LenPrefixSize + len(packet)

	if needed > c.maxSize {
		// Oversized packet — send directly without coalescing.
		if c.offset > 0 {
			c.flushLocked()
		}
		c.flushFn(packet)
		return
	}

	if c.offset+needed > c.maxSize {
		c.flushLocked()
	}

	binary.BigEndian.PutUint16(c.buf[c.offset:], uint16(len(packet)))
	copy(c.buf[c.offset+LenPrefixSize:], packet)
	c.offset += needed

	if c.offset == needed {
		// First packet in buffer — start the flush timer.
		c.timer.Reset(c.interval)
	}
}

// Flush sends any buffered packets immediately.
func (c *Coalescer) Flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flushLocked()
}

func (c *Coalescer) flushLocked() {
	if c.offset == 0 {
		return
	}
	c.timer.Stop()

	out := make([]byte, c.offset)
	copy(out, c.buf[:c.offset])
	c.offset = 0

	c.flushFn(out)
}

func (c *Coalescer) timerFlush() {
	c.Flush()
}

// Close stops the flush timer.
func (c *Coalescer) Close() {
	c.timer.Stop()
}

// IsCoalesced checks if a received UDP datagram contains coalesced packets
// (length-prefixed) vs a single Nebula packet (starts with version nibble).
func IsCoalesced(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	// Nebula v1 header starts with version=1 in upper nibble: 0x1X.
	// A 2-byte big-endian length prefix for a typical packet (16-1440 bytes)
	// will have first byte 0x00-0x05 — never matches 0x1X.
	return data[0]&0xF0 != magicVersion
}

// Split separates a coalesced datagram into individual packets.
// If the datagram is not coalesced (single Nebula packet), returns it as-is.
func Split(data []byte) [][]byte {
	if !IsCoalesced(data) {
		return [][]byte{data}
	}

	var packets [][]byte
	offset := 0
	for offset+LenPrefixSize <= len(data) {
		pktLen := int(binary.BigEndian.Uint16(data[offset:]))
		offset += LenPrefixSize
		if offset+pktLen > len(data) {
			break
		}
		packets = append(packets, data[offset:offset+pktLen])
		offset += pktLen
	}
	return packets
}
