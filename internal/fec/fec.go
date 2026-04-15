package fec

import (
	"log"
	"net/netip"
	"sync"
	"time"

	"github.com/klauspost/reedsolomon"
	"github.com/slackhq/nebula/config"
	"github.com/slackhq/nebula/udp"
)

type Config struct {
	DataShards   int
	ParityShards int
	GroupTimeout time.Duration
}

func DefaultConfig() Config {
	return Config{
		DataShards:   10,
		ParityShards: 2,
		GroupTimeout: 50 * time.Millisecond,
	}
}

type sendGroup struct {
	id      uint16
	shards  [][]byte
	count   int
	maxLen  int
	timer   *time.Timer
}

type recvGroup struct {
	shards   [][]byte
	present  []bool
	received int
	maxLen   int
	created  time.Time
	k        int
	m        int
}

type Conn struct {
	inner  udp.Conn
	config Config
	enc    reedsolomon.Encoder

	sendMu  sync.Mutex
	groups  map[netip.AddrPort]*sendGroup
	nextGID uint16

	recvMu  sync.Mutex
	pending map[uint16]*recvGroup
}

func NewConn(inner udp.Conn, cfg Config) *Conn {
	enc, err := reedsolomon.New(cfg.DataShards, cfg.ParityShards)
	if err != nil {
		log.Fatalf("[fec] failed to create encoder: %v", err)
	}
	c := &Conn{
		inner:   inner,
		config:  cfg,
		enc:     enc,
		groups:  make(map[netip.AddrPort]*sendGroup),
		pending: make(map[uint16]*recvGroup),
	}
	go c.cleanupLoop()
	return c
}

func (c *Conn) WriteTo(b []byte, addr netip.AddrPort) error {
	c.sendMu.Lock()

	g, ok := c.groups[addr]
	if !ok {
		gid := c.nextGID
		c.nextGID++
		g = &sendGroup{
			id:     gid,
			shards: make([][]byte, c.config.DataShards),
		}
		g.timer = time.AfterFunc(c.config.GroupTimeout, func() {
			c.flushGroup(addr)
		})
		c.groups[addr] = g
	}

	pkt := make([]byte, len(b))
	copy(pkt, b)
	g.shards[g.count] = pkt
	g.count++
	if len(pkt) > g.maxLen {
		g.maxLen = len(pkt)
	}

	if g.count >= c.config.DataShards {
		c.encodeAndSendLocked(g, addr)
		delete(c.groups, addr)
	}

	c.sendMu.Unlock()
	return nil
}

func (c *Conn) flushGroup(addr netip.AddrPort) {
	c.sendMu.Lock()
	g, ok := c.groups[addr]
	if ok && g.count > 0 {
		c.encodeAndSendLocked(g, addr)
		delete(c.groups, addr)
	}
	c.sendMu.Unlock()
}

func (c *Conn) encodeAndSendLocked(g *sendGroup, addr netip.AddrPort) {
	g.timer.Stop()
	k := g.count
	m := c.config.ParityShards

	// Pad all shards to same length
	for i := 0; i < k; i++ {
		if len(g.shards[i]) < g.maxLen {
			padded := make([]byte, g.maxLen)
			copy(padded, g.shards[i])
			g.shards[i] = padded
		}
	}

	// Create full shard array: k data + m parity
	total := k + m
	allShards := make([][]byte, total)
	for i := 0; i < k; i++ {
		allShards[i] = g.shards[i]
	}
	for i := k; i < total; i++ {
		allShards[i] = make([]byte, g.maxLen)
	}

	// Use a temporary encoder if k differs from configured (partial group)
	var enc reedsolomon.Encoder
	if k == c.config.DataShards {
		enc = c.enc
	} else {
		var err error
		enc, err = reedsolomon.New(k, m)
		if err != nil {
			// Can't encode — send data packets raw
			for i := 0; i < k; i++ {
				c.inner.WriteTo(g.shards[i], addr)
			}
			return
		}
	}

	if err := enc.Encode(allShards); err != nil {
		// Encode failed — send data packets raw
		for i := 0; i < k; i++ {
			c.inner.WriteTo(g.shards[i], addr)
		}
		return
	}

	// Send each shard with FEC header
	hdr := Header{
		GroupID:     g.id,
		DataCount:   uint8(k),
		ParityCount: uint8(m),
	}
	hdrBuf := make([]byte, HeaderSize)

	for i := 0; i < total; i++ {
		hdr.Index = uint8(i)
		hdr.Marshal(hdrBuf)

		// Prepend header to shard
		pkt := make([]byte, HeaderSize+len(allShards[i]))
		copy(pkt, hdrBuf)
		copy(pkt[HeaderSize:], allShards[i])

		c.inner.WriteTo(pkt, addr)
	}
}

func (c *Conn) ListenOut(r udp.EncReader) {
	c.inner.ListenOut(func(addr netip.AddrPort, payload []byte) {
		if !IsFEC(payload) {
			// Raw Nebula packet (backward compat)
			r(addr, payload)
			return
		}

		hdr, ok := ParseHeader(payload)
		if !ok {
			return
		}

		data := payload[HeaderSize:]
		k := int(hdr.DataCount)
		m := int(hdr.ParityCount)
		total := k + m

		c.recvMu.Lock()

		rg, ok := c.pending[hdr.GroupID]
		if !ok {
			rg = &recvGroup{
				shards:  make([][]byte, total),
				present: make([]bool, total),
				created: time.Now(),
				k:       k,
				m:       m,
			}
			c.pending[hdr.GroupID] = rg
		}

		idx := int(hdr.Index)
		if idx >= total || rg.present[idx] {
			c.recvMu.Unlock()
			return
		}

		shard := make([]byte, len(data))
		copy(shard, data)
		rg.shards[idx] = shard
		rg.present[idx] = true
		rg.received++
		if len(shard) > rg.maxLen {
			rg.maxLen = len(shard)
		}

		if rg.received >= k {
			// Can decode — reconstruct missing shards
			recovered := c.tryDecode(rg)
			delete(c.pending, hdr.GroupID)
			c.recvMu.Unlock()

			for _, pkt := range recovered {
				r(addr, pkt)
			}
			return
		}

		c.recvMu.Unlock()
	})
}

func (c *Conn) tryDecode(rg *recvGroup) [][]byte {
	k := rg.k
	m := rg.m
	total := k + m

	// Pad all present shards to maxLen
	for i := 0; i < total; i++ {
		if rg.present[i] && len(rg.shards[i]) < rg.maxLen {
			padded := make([]byte, rg.maxLen)
			copy(padded, rg.shards[i])
			rg.shards[i] = padded
		}
	}

	// Set missing shards to nil for Reed-Solomon
	for i := 0; i < total; i++ {
		if !rg.present[i] {
			rg.shards[i] = nil
		}
	}

	enc, err := reedsolomon.New(k, m)
	if err != nil {
		// Return what we have
		var result [][]byte
		for i := 0; i < k; i++ {
			if rg.present[i] {
				result = append(result, rg.shards[i])
			}
		}
		return result
	}

	if err := enc.Reconstruct(rg.shards); err != nil {
		var result [][]byte
		for i := 0; i < k; i++ {
			if rg.present[i] {
				result = append(result, rg.shards[i])
			}
		}
		return result
	}

	// Return all k data shards (now reconstructed)
	result := make([][]byte, k)
	copy(result, rg.shards[:k])
	return result
}

func (c *Conn) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		c.recvMu.Lock()
		now := time.Now()
		for id, rg := range c.pending {
			if now.Sub(rg.created) > 2*time.Second {
				delete(c.pending, id)
			}
		}
		c.recvMu.Unlock()
	}
}

func (c *Conn) Rebind() error                      { return c.inner.Rebind() }
func (c *Conn) LocalAddr() (netip.AddrPort, error) { return c.inner.LocalAddr() }
func (c *Conn) ReloadConfig(cfg *config.C)         { c.inner.ReloadConfig(cfg) }
func (c *Conn) SupportsMultipleReaders() bool      { return c.inner.SupportsMultipleReaders() }

func (c *Conn) Close() error {
	c.sendMu.Lock()
	for _, g := range c.groups {
		g.timer.Stop()
	}
	c.sendMu.Unlock()
	return c.inner.Close()
}
