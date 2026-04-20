package portmap

import (
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
	"time"
)

// mockNATPMPServer runs a UDP server on 127.0.0.1 that responds to NAT-PMP
// requests with caller-supplied behavior. Used to drive hermetic unit
// tests without needing a real router.
type mockNATPMPServer struct {
	conn       *net.UDPConn
	handleFunc func(req []byte) (resp []byte, shouldReply bool)
}

func newMockNATPMPServer(t *testing.T, handle func(req []byte) (resp []byte, shouldReply bool)) *mockNATPMPServer {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("mock listen: %v", err)
	}
	s := &mockNATPMPServer{conn: conn, handleFunc: handle}
	go s.serve()
	t.Cleanup(func() { conn.Close() })
	return s
}

func (s *mockNATPMPServer) serve() {
	buf := make([]byte, 256)
	for {
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		resp, shouldReply := s.handleFunc(buf[:n])
		if shouldReply {
			_, _ = s.conn.WriteToUDP(resp, addr)
		}
	}
}

func (s *mockNATPMPServer) Port() uint16 {
	return uint16(s.conn.LocalAddr().(*net.UDPAddr).Port)
}

// newClientForMock returns a NATPMP client pointing at a mock server.
// We override the default port 5351 by giving the client a gateway that
// dial-hooks into the mock. Since the mock listens on 127.0.0.1:<port>,
// we need to send to that exact port — but NATPMP always dials port 5351.
// Solution: for tests, we redial through a connected UDP socket that
// forwards to the mock. Simpler: expose a test-only hook on NATPMP.
//
// To keep tests clean without polluting the production struct, we use a
// package-level test seam via a variable natpmpDialAddr that the client
// consults in test mode. Production code always uses the default.

func makeResponse(op byte, resultCode uint16, epoch uint32, extra []byte) []byte {
	r := make([]byte, 8+len(extra))
	r[0] = 0 // version
	r[1] = natpmpResponseOpBit | op
	binary.BigEndian.PutUint16(r[2:4], resultCode)
	binary.BigEndian.PutUint32(r[4:8], epoch)
	copy(r[8:], extra)
	return r
}

func extAddrResponse(resultCode uint16, pubIP [4]byte) []byte {
	return makeResponse(natpmpOpExternalAddr, resultCode, 1, pubIP[:])
}

func mapResponse(resultCode uint16, intPort, extPort uint16, lifetime uint32) []byte {
	body := make([]byte, 8)
	binary.BigEndian.PutUint16(body[0:2], intPort)
	binary.BigEndian.PutUint16(body[2:4], extPort)
	binary.BigEndian.PutUint32(body[4:8], lifetime)
	return makeResponse(natpmpOpMapUDP, resultCode, 1, body)
}

func TestNATPMP_Map_Success(t *testing.T) {
	server := newMockNATPMPServer(t, func(req []byte) ([]byte, bool) {
		if len(req) == 2 && req[1] == natpmpOpExternalAddr {
			return extAddrResponse(natpmpRCSuccess, [4]byte{203, 0, 113, 5}), true
		}
		if len(req) == 12 && req[1] == natpmpOpMapUDP {
			reqInt := binary.BigEndian.Uint16(req[4:6])
			return mapResponse(natpmpRCSuccess, reqInt, reqInt, 7200), true
		}
		return nil, false
	})

	testOverrideDialPort = server.Port()
	t.Cleanup(func() { testOverrideDialPort = 0 })

	c := NewNATPMP(netip.MustParseAddr("127.0.0.1"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pub, ttl, err := c.Map(ctx, 4242)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if got, want := pub.String(), "203.0.113.5:4242"; got != want {
		t.Errorf("public = %s, want %s", got, want)
	}
	if ttl != 7200*time.Second {
		t.Errorf("ttl = %v, want 2h", ttl)
	}
}

func TestNATPMP_Map_NonZeroResultCode(t *testing.T) {
	server := newMockNATPMPServer(t, func(req []byte) ([]byte, bool) {
		if len(req) == 2 {
			return extAddrResponse(natpmpRCNotAuthorized, [4]byte{}), true
		}
		return nil, false
	})
	testOverrideDialPort = server.Port()
	t.Cleanup(func() { testOverrideDialPort = 0 })

	c := NewNATPMP(netip.MustParseAddr("127.0.0.1"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, _, err := c.Map(ctx, 4242)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestNATPMP_Map_NoResponse(t *testing.T) {
	// Server listens but never replies — exercises timeout path.
	server := newMockNATPMPServer(t, func(req []byte) ([]byte, bool) {
		return nil, false
	})
	testOverrideDialPort = server.Port()
	t.Cleanup(func() { testOverrideDialPort = 0 })

	c := NewNATPMP(netip.MustParseAddr("127.0.0.1"))
	// Short ctx to avoid running through all 9 retries (64 s total).
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	_, _, err := c.Map(ctx, 4242)
	if err == nil {
		t.Fatal("expected error on no-reply, got nil")
	}
}

func TestNATPMP_Map_ContextCancelled(t *testing.T) {
	server := newMockNATPMPServer(t, func(req []byte) ([]byte, bool) {
		return nil, false
	})
	testOverrideDialPort = server.Port()
	t.Cleanup(func() { testOverrideDialPort = 0 })

	c := NewNATPMP(netip.MustParseAddr("127.0.0.1"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := c.Map(ctx, 4242)
	if err == nil {
		t.Fatal("expected error on cancelled ctx, got nil")
	}
}

func TestNATPMP_Map_ZeroPublicIP(t *testing.T) {
	// Router reports 0.0.0.0 as public IP → usually double-NAT.
	server := newMockNATPMPServer(t, func(req []byte) ([]byte, bool) {
		if len(req) == 2 {
			return extAddrResponse(natpmpRCSuccess, [4]byte{0, 0, 0, 0}), true
		}
		return nil, false
	})
	testOverrideDialPort = server.Port()
	t.Cleanup(func() { testOverrideDialPort = 0 })

	c := NewNATPMP(netip.MustParseAddr("127.0.0.1"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, _, err := c.Map(ctx, 4242)
	if err == nil {
		t.Fatal("expected error for 0.0.0.0 public IP, got nil")
	}
}

func TestNATPMP_Map_MismatchedInternalPort(t *testing.T) {
	// Router echoes a different internal port than we asked for → bug.
	server := newMockNATPMPServer(t, func(req []byte) ([]byte, bool) {
		if len(req) == 2 {
			return extAddrResponse(natpmpRCSuccess, [4]byte{203, 0, 113, 5}), true
		}
		if len(req) == 12 {
			return mapResponse(natpmpRCSuccess, 9999 /* wrong */, 4242, 7200), true
		}
		return nil, false
	})
	testOverrideDialPort = server.Port()
	t.Cleanup(func() { testOverrideDialPort = 0 })

	c := NewNATPMP(netip.MustParseAddr("127.0.0.1"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, _, err := c.Map(ctx, 4242)
	if err == nil {
		t.Fatal("expected error on internal-port mismatch")
	}
}
