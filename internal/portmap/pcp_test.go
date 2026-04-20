package portmap

import (
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
	"time"
)

// pcpMockServer is a UDP server that runs a caller-supplied handler
// for each received PCP request. Hermetic equivalent of mockNATPMPServer.
type pcpMockServer struct {
	conn    *net.UDPConn
	handler func(req []byte) (resp []byte, shouldReply bool)
}

func newPCPMockServer(t *testing.T, handler func(req []byte) (resp []byte, shouldReply bool)) *pcpMockServer {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("pcp mock listen: %v", err)
	}
	s := &pcpMockServer{conn: conn, handler: handler}
	go s.serve()
	t.Cleanup(func() { conn.Close() })
	return s
}

func (s *pcpMockServer) Port() uint16 {
	return uint16(s.conn.LocalAddr().(*net.UDPAddr).Port)
}

func (s *pcpMockServer) serve() {
	buf := make([]byte, 4096)
	for {
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		resp, ok := s.handler(buf[:n])
		if ok {
			_, _ = s.conn.WriteToUDP(resp, addr)
		}
	}
}

// buildPCPMapResponseFor synthesizes a successful MAP response for the
// given request. Echoes the nonce + protocol per spec.
func buildPCPMapResponseFor(req []byte, externalPort uint16, externalIPv4 [4]byte, lifetime uint32) []byte {
	resp := make([]byte, pcpReqLen)
	resp[0] = pcpVersion
	resp[1] = pcpResponseRBit | pcpOpcodeMAP
	resp[3] = pcpRCSuccess
	binary.BigEndian.PutUint32(resp[4:8], lifetime)
	// Epoch (bytes 8-11): leave zero; clients don't validate freshness here.
	// Bytes 12-23: reserved.
	// MAP payload starts at byte 24:
	copy(resp[24:36], req[24:36]) // echo nonce
	resp[36] = pcpProtoUDP
	intPort := binary.BigEndian.Uint16(req[40:42])
	binary.BigEndian.PutUint16(resp[40:42], intPort)
	binary.BigEndian.PutUint16(resp[42:44], externalPort)
	// External IP: IPv4-mapped IPv6 (::ffff:x.y.z.w)
	resp[54] = 0xff
	resp[55] = 0xff
	copy(resp[56:60], externalIPv4[:])
	return resp
}

func TestPCP_Map_Success(t *testing.T) {
	want := [4]byte{203, 0, 113, 5}
	server := newPCPMockServer(t, func(req []byte) ([]byte, bool) {
		return buildPCPMapResponseFor(req, 4242, want, 7200), true
	})
	testOverrideDialPort = server.Port()
	t.Cleanup(func() { testOverrideDialPort = 0 })

	c := NewPCP(netip.MustParseAddr("127.0.0.1"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pub, ttl, err := c.Map(ctx, 4242)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if pub.String() != "203.0.113.5:4242" {
		t.Errorf("public = %s, want 203.0.113.5:4242", pub)
	}
	if ttl != 7200*time.Second {
		t.Errorf("ttl = %v", ttl)
	}
}

func TestPCP_Map_VersionMismatchError(t *testing.T) {
	// Server speaks NAT-PMP only — replies VER=0 RC=1 (UNSUPP_VERSION).
	server := newPCPMockServer(t, func(req []byte) ([]byte, bool) {
		resp := make([]byte, pcpReqLen)
		resp[0] = 0 // NAT-PMP version
		resp[1] = pcpResponseRBit | pcpOpcodeMAP
		resp[3] = pcpRCUnsupportedVersion
		return resp, true
	})
	testOverrideDialPort = server.Port()
	t.Cleanup(func() { testOverrideDialPort = 0 })

	c := NewPCP(netip.MustParseAddr("127.0.0.1"))
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if _, _, err := c.Map(ctx, 4242); err == nil {
		t.Fatal("expected error on VER=0 / RC=1 reply")
	}
}

func TestPCP_Map_NonceMismatchRejected(t *testing.T) {
	// Server scrambles the nonce in response — client must refuse.
	server := newPCPMockServer(t, func(req []byte) ([]byte, bool) {
		resp := buildPCPMapResponseFor(req, 4242, [4]byte{1, 2, 3, 4}, 7200)
		// Corrupt one nonce byte.
		resp[24] ^= 0xff
		return resp, true
	})
	testOverrideDialPort = server.Port()
	t.Cleanup(func() { testOverrideDialPort = 0 })

	c := NewPCP(netip.MustParseAddr("127.0.0.1"))
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if _, _, err := c.Map(ctx, 4242); err == nil {
		t.Fatal("expected error on nonce mismatch")
	}
}

func TestPCP_Map_NoResponse(t *testing.T) {
	server := newPCPMockServer(t, func(req []byte) ([]byte, bool) { return nil, false })
	testOverrideDialPort = server.Port()
	t.Cleanup(func() { testOverrideDialPort = 0 })

	c := NewPCP(netip.MustParseAddr("127.0.0.1"))
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if _, _, err := c.Map(ctx, 4242); err == nil {
		t.Fatal("expected error on no-reply")
	}
}

func TestPCP_Map_RejectsZeroExternalIP(t *testing.T) {
	server := newPCPMockServer(t, func(req []byte) ([]byte, bool) {
		// Successful response but external IP all zeros (double-NAT case).
		return buildPCPMapResponseFor(req, 4242, [4]byte{0, 0, 0, 0}, 7200), true
	})
	testOverrideDialPort = server.Port()
	t.Cleanup(func() { testOverrideDialPort = 0 })

	c := NewPCP(netip.MustParseAddr("127.0.0.1"))
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if _, _, err := c.Map(ctx, 4242); err == nil {
		t.Fatal("expected error on 0.0.0.0 external IP")
	}
}

func TestIPv4MappedIPv6_RoundTrip(t *testing.T) {
	in := netip.MustParseAddr("203.0.113.5")
	mapped := ipv4MappedIPv6(in)
	if len(mapped) != 16 {
		t.Fatalf("len = %d, want 16", len(mapped))
	}
	if mapped[10] != 0xff || mapped[11] != 0xff {
		t.Errorf("missing ::ffff: prefix")
	}
	out, err := pcpExtractIPv4FromIPv6(mapped)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if out != in {
		t.Errorf("round-trip: got %s want %s", out, in)
	}
}

func TestPCPExtractIPv4_RejectsNonMapped(t *testing.T) {
	// Pure IPv6 address (not IPv4-mapped).
	b := make([]byte, 16)
	b[0] = 0x20 // 2000::
	b[1] = 0x01
	if _, err := pcpExtractIPv4FromIPv6(b); err == nil {
		t.Errorf("expected error for non-IPv4-mapped IPv6")
	}
}
