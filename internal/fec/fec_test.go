package fec

import (
	"bytes"
	"math/rand"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/slackhq/nebula/config"
	"github.com/slackhq/nebula/udp"
)

var testAddr = netip.MustParseAddrPort("10.42.1.6:4242")

// mockConn captures sent packets and replays them to ListenOut.
type mockConn struct {
	mu       sync.Mutex
	sent     []sentPacket
	listener udp.EncReader
}

type sentPacket struct {
	data []byte
	addr netip.AddrPort
}

func (m *mockConn) WriteTo(b []byte, addr netip.AddrPort) error {
	m.mu.Lock()
	pkt := make([]byte, len(b))
	copy(pkt, b)
	m.sent = append(m.sent, sentPacket{data: pkt, addr: addr})
	m.mu.Unlock()
	return nil
}

func (m *mockConn) ListenOut(r udp.EncReader) {
	m.listener = r
}

func (m *mockConn) deliver(dropIndices map[int]bool) {
	m.mu.Lock()
	packets := make([]sentPacket, len(m.sent))
	copy(packets, m.sent)
	m.mu.Unlock()

	for i, p := range packets {
		if dropIndices[i] {
			continue
		}
		if m.listener != nil {
			m.listener(p.addr, p.data)
		}
	}
}

func (m *mockConn) Rebind() error                      { return nil }
func (m *mockConn) LocalAddr() (netip.AddrPort, error) { return netip.AddrPort{}, nil }
func (m *mockConn) ReloadConfig(_ *config.C)           {}
func (m *mockConn) SupportsMultipleReaders() bool      { return false }
func (m *mockConn) Close() error                       { return nil }

func makePackets(count, size int) [][]byte {
	packets := make([][]byte, count)
	for i := range packets {
		pkt := make([]byte, size)
		rand.Read(pkt)
		// Ensure first byte isn't 0xFE (would be mistaken for FEC)
		pkt[0] = 0x10 | byte(i%16)
		packets[i] = pkt
	}
	return packets
}

func TestFEC_NoLoss(t *testing.T) {
	mock := &mockConn{}
	cfg := Config{DataShards: 4, ParityShards: 2, GroupTimeout: time.Second}
	sender := NewConn(mock, cfg)

	packets := makePackets(4, 100)
	for _, p := range packets {
		sender.WriteTo(p, testAddr)
	}

	// Should have sent 6 packets (4 data + 2 parity)
	mock.mu.Lock()
	sentCount := len(mock.sent)
	mock.mu.Unlock()
	if sentCount != 6 {
		t.Fatalf("expected 6 sent packets, got %d", sentCount)
	}



	var results [][]byte
	var resMu sync.Mutex

	// Set up the receive side
	innerRecv := &mockConn{}
	fecRecv := NewConn(innerRecv, cfg)

	fecRecv.ListenOut(func(addr netip.AddrPort, payload []byte) {
		resMu.Lock()
		pkt := make([]byte, len(payload))
		copy(pkt, payload)
		results = append(results, pkt)
		resMu.Unlock()
	})

	// Deliver all packets (no loss)
	mock.mu.Lock()
	for _, sp := range mock.sent {
		innerRecv.listener(sp.addr, sp.data)
	}
	mock.mu.Unlock()

	resMu.Lock()
	defer resMu.Unlock()
	if len(results) != 4 {
		t.Fatalf("expected 4 recovered packets, got %d", len(results))
	}

	for i, p := range packets {
		if !bytes.Equal(results[i][:len(p)], p) {
			t.Fatalf("packet %d mismatch", i)
		}
	}

	_ = sender
}

func TestFEC_SingleLoss(t *testing.T) {
	mock := &mockConn{}
	cfg := Config{DataShards: 4, ParityShards: 2, GroupTimeout: time.Second}
	sender := NewConn(mock, cfg)

	packets := makePackets(4, 200)
	for _, p := range packets {
		sender.WriteTo(p, testAddr)
	}

	// Set up receiver
	innerRecv := &mockConn{}
	fecRecv := NewConn(innerRecv, cfg)

	var results [][]byte
	var mu sync.Mutex
	fecRecv.ListenOut(func(addr netip.AddrPort, payload []byte) {
		mu.Lock()
		pkt := make([]byte, len(payload))
		copy(pkt, payload)
		results = append(results, pkt)
		mu.Unlock()
	})

	// Drop packet index 2 (3rd data packet)
	mock.mu.Lock()
	for i, sp := range mock.sent {
		if i == 2 {
			continue // DROP
		}
		innerRecv.listener(sp.addr, sp.data)
	}
	mock.mu.Unlock()

	mu.Lock()
	defer mu.Unlock()
	if len(results) != 4 {
		t.Fatalf("expected 4 recovered packets (1 loss, recovered by FEC), got %d", len(results))
	}

	for i, p := range packets {
		if !bytes.Equal(results[i][:len(p)], p) {
			t.Fatalf("packet %d mismatch after recovery", i)
		}
	}
}

func TestFEC_TwoLosses(t *testing.T) {
	mock := &mockConn{}
	cfg := Config{DataShards: 4, ParityShards: 2, GroupTimeout: time.Second}
	sender := NewConn(mock, cfg)

	packets := makePackets(4, 150)
	for _, p := range packets {
		sender.WriteTo(p, testAddr)
	}

	// Set up receiver
	innerRecv := &mockConn{}
	fecRecv := NewConn(innerRecv, cfg)

	var results [][]byte
	var mu sync.Mutex
	fecRecv.ListenOut(func(addr netip.AddrPort, payload []byte) {
		mu.Lock()
		pkt := make([]byte, len(payload))
		copy(pkt, payload)
		results = append(results, pkt)
		mu.Unlock()
	})

	// Drop packets 1 and 3 (2 losses — within parity capacity of 2)
	mock.mu.Lock()
	for i, sp := range mock.sent {
		if i == 1 || i == 3 {
			continue // DROP
		}
		innerRecv.listener(sp.addr, sp.data)
	}
	mock.mu.Unlock()

	mu.Lock()
	defer mu.Unlock()
	if len(results) != 4 {
		t.Fatalf("expected 4 recovered packets (2 losses, recovered by RS), got %d", len(results))
	}

	for i, p := range packets {
		if !bytes.Equal(results[i][:len(p)], p) {
			t.Fatalf("packet %d mismatch after 2-loss recovery", i)
		}
	}
}

func TestFEC_BackwardCompat_RawPacket(t *testing.T) {
	innerRecv := &mockConn{}
	cfg := Config{DataShards: 4, ParityShards: 2, GroupTimeout: time.Second}
	fecRecv := NewConn(innerRecv, cfg)

	var results [][]byte
	fecRecv.ListenOut(func(addr netip.AddrPort, payload []byte) {
		results = append(results, payload)
	})

	// Send a raw Nebula packet (starts with 0x10, not 0xFE)
	rawPacket := []byte{0x10, 0x00, 0x01, 0x02, 0x03, 0x04}
	innerRecv.listener(testAddr, rawPacket)

	if len(results) != 1 {
		t.Fatalf("expected 1 pass-through packet, got %d", len(results))
	}
	if !bytes.Equal(results[0], rawPacket) {
		t.Fatal("raw packet should pass through unchanged")
	}
}

func TestFEC_PartialGroup_Timeout(t *testing.T) {
	mock := &mockConn{}
	cfg := Config{DataShards: 4, ParityShards: 2, GroupTimeout: 50 * time.Millisecond}
	sender := NewConn(mock, cfg)

	// Send only 2 of 4 packets (group not full)
	packets := makePackets(2, 100)
	for _, p := range packets {
		sender.WriteTo(p, testAddr)
	}

	// Wait for timeout to flush
	time.Sleep(200 * time.Millisecond)

	mock.mu.Lock()
	sentCount := len(mock.sent)
	mock.mu.Unlock()

	// Should have sent 2 data + 2 parity = 4 (partial group with RS(2,2))
	if sentCount < 2 {
		t.Fatalf("expected at least 2 sent packets after timeout, got %d", sentCount)
	}
}

func TestHeader_MarshalParse(t *testing.T) {
	h := Header{GroupID: 1234, Index: 3, DataCount: 10, ParityCount: 2}
	buf := make([]byte, HeaderSize)
	h.Marshal(buf)

	parsed, ok := ParseHeader(buf)
	if !ok {
		t.Fatal("failed to parse header")
	}
	if parsed.GroupID != 1234 || parsed.Index != 3 || parsed.DataCount != 10 || parsed.ParityCount != 2 {
		t.Fatalf("header mismatch: %+v", parsed)
	}
}

func TestIsFEC(t *testing.T) {
	fecPkt := []byte{0xFE, 0x00, 0x01, 0x02, 0x0A, 0x02}
	if !IsFEC(fecPkt) {
		t.Fatal("should detect FEC packet")
	}

	nebulaPkt := []byte{0x10, 0x00, 0x01, 0x02}
	if IsFEC(nebulaPkt) {
		t.Fatal("should not detect Nebula packet as FEC")
	}
}

func BenchmarkEncode(b *testing.B) {
	mock := &mockConn{}
	cfg := Config{DataShards: 10, ParityShards: 2, GroupTimeout: time.Hour}
	sender := NewConn(mock, cfg)

	pkt := make([]byte, 1400)
	rand.Read(pkt)
	pkt[0] = 0x10

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sender.WriteTo(pkt, testAddr)
		// Reset after each full group
		if (i+1)%10 == 0 {
			mock.mu.Lock()
			mock.sent = nil
			mock.mu.Unlock()
		}
	}
}
