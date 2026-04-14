package coalesce

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

func TestCoalescer_SinglePacket(t *testing.T) {
	var got []byte
	var mu sync.Mutex
	c := NewCoalescer(func(data []byte) {
		mu.Lock()
		got = append([]byte{}, data...)
		mu.Unlock()
	})
	defer c.Close()

	pkt := []byte{0x10, 0x00, 0x01, 0x02, 0x03} // looks like Nebula header
	c.Add(pkt)
	c.Flush()

	mu.Lock()
	defer mu.Unlock()
	if got == nil {
		t.Fatal("expected flush output")
	}

	packets := Split(got)
	if len(packets) != 1 {
		t.Fatalf("expected 1 packet, got %d", len(packets))
	}
	if !bytes.Equal(packets[0], pkt) {
		t.Fatalf("packet mismatch: %x vs %x", packets[0], pkt)
	}
}

func TestCoalescer_MultiplePackets(t *testing.T) {
	var got []byte
	var mu sync.Mutex
	c := NewCoalescer(func(data []byte) {
		mu.Lock()
		got = append([]byte{}, data...)
		mu.Unlock()
	})
	defer c.Close()

	pkt1 := bytes.Repeat([]byte{0xAA}, 100)
	pkt2 := bytes.Repeat([]byte{0xBB}, 200)
	pkt3 := bytes.Repeat([]byte{0xCC}, 50)

	c.Add(pkt1)
	c.Add(pkt2)
	c.Add(pkt3)
	c.Flush()

	mu.Lock()
	defer mu.Unlock()

	if !IsCoalesced(got) {
		t.Fatal("expected coalesced datagram")
	}

	packets := Split(got)
	if len(packets) != 3 {
		t.Fatalf("expected 3 packets, got %d", len(packets))
	}
	if !bytes.Equal(packets[0], pkt1) {
		t.Fatal("packet 0 mismatch")
	}
	if !bytes.Equal(packets[1], pkt2) {
		t.Fatal("packet 1 mismatch")
	}
	if !bytes.Equal(packets[2], pkt3) {
		t.Fatal("packet 2 mismatch")
	}
}

func TestCoalescer_AutoFlushOnFull(t *testing.T) {
	flushCount := 0
	var mu sync.Mutex
	c := NewCoalescerWithConfig(func(data []byte) {
		mu.Lock()
		flushCount++
		mu.Unlock()
	}, 100, time.Hour) // very long timer so only buffer-full triggers flush
	defer c.Close()

	// Each packet: 2 (prefix) + 40 (data) = 42 bytes. 100/42 = 2 per batch.
	pkt := bytes.Repeat([]byte{0xDD}, 40)

	c.Add(pkt) // 42 bytes used
	c.Add(pkt) // 84 bytes used
	// Next add would exceed 100 → auto flush.
	c.Add(pkt)

	mu.Lock()
	defer mu.Unlock()
	if flushCount < 1 {
		t.Fatalf("expected at least 1 auto-flush, got %d", flushCount)
	}
}

func TestCoalescer_TimerFlush(t *testing.T) {
	var got []byte
	var mu sync.Mutex
	done := make(chan struct{})
	c := NewCoalescerWithConfig(func(data []byte) {
		mu.Lock()
		got = append([]byte{}, data...)
		mu.Unlock()
		select {
		case done <- struct{}{}:
		default:
		}
	}, MaxDatagramSize, 5*time.Millisecond)
	defer c.Close()

	pkt := []byte{0x01, 0x02, 0x03}
	c.Add(pkt)

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timer flush did not fire within 100ms")
	}

	mu.Lock()
	defer mu.Unlock()
	packets := Split(got)
	if len(packets) != 1 {
		t.Fatalf("expected 1 packet, got %d", len(packets))
	}
}

func TestCoalescer_OversizedPacket(t *testing.T) {
	var results [][]byte
	var mu sync.Mutex
	c := NewCoalescerWithConfig(func(data []byte) {
		mu.Lock()
		results = append(results, append([]byte{}, data...))
		mu.Unlock()
	}, 100, time.Hour)
	defer c.Close()

	// Packet larger than max datagram size — sent directly.
	big := bytes.Repeat([]byte{0xFF}, 200)
	c.Add(big)

	mu.Lock()
	defer mu.Unlock()
	if len(results) != 1 {
		t.Fatalf("expected 1 direct send, got %d", len(results))
	}
	if !bytes.Equal(results[0], big) {
		t.Fatal("oversized packet should be sent as-is")
	}
}

func TestIsCoalesced(t *testing.T) {
	// Nebula v1 header: first byte has upper nibble = 1 (0x1X).
	nebulaPacket := []byte{0x10, 0x00, 0x00, 0x00, 0x01, 0x02}
	if IsCoalesced(nebulaPacket) {
		t.Fatal("Nebula packet should not be detected as coalesced")
	}

	// Coalesced: first 2 bytes are a length prefix. For a 100-byte packet,
	// prefix is 0x00 0x64 — first byte 0x00, upper nibble 0x0 ≠ 0x1.
	coalesced := []byte{0x00, 0x64}
	coalesced = append(coalesced, bytes.Repeat([]byte{0xAA}, 100)...)
	if !IsCoalesced(coalesced) {
		t.Fatal("length-prefixed data should be detected as coalesced")
	}
}

func TestSplit_SingleNebulaPacket(t *testing.T) {
	pkt := []byte{0x10, 0x00, 0x00, 0x00, 0x01, 0x02, 0x03}
	packets := Split(pkt)
	if len(packets) != 1 {
		t.Fatalf("expected 1 packet, got %d", len(packets))
	}
	if !bytes.Equal(packets[0], pkt) {
		t.Fatal("should return original packet unchanged")
	}
}

func TestSplit_TruncatedData(t *testing.T) {
	// Length prefix says 100 bytes but only 10 available.
	data := []byte{0x00, 0x64}
	data = append(data, bytes.Repeat([]byte{0xAA}, 10)...)
	packets := Split(data)
	if len(packets) != 0 {
		t.Fatalf("expected 0 packets from truncated data, got %d", len(packets))
	}
}

func BenchmarkCoalescer_Add(b *testing.B) {
	c := NewCoalescer(func(data []byte) {})
	defer c.Close()
	pkt := bytes.Repeat([]byte{0xAA}, 200)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Add(pkt)
	}
}

func BenchmarkSplit(b *testing.B) {
	// Build a coalesced datagram with 5 packets.
	c := NewCoalescer(func(data []byte) {})
	defer c.Close()
	pkt := bytes.Repeat([]byte{0xAA}, 200)
	for i := 0; i < 5; i++ {
		c.Add(pkt)
	}
	c.Flush()

	// Get the flushed data by building it manually.
	var buf []byte
	c2 := NewCoalescer(func(data []byte) { buf = append([]byte{}, data...) })
	for i := 0; i < 5; i++ {
		c2.Add(pkt)
	}
	c2.Flush()
	c2.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Split(buf)
	}
}
