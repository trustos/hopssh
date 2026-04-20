package portmap

// PCP — Port Control Protocol, RFC 6887. NAT-PMP's successor. Same
// UDP port (5351) and same socket discipline as NAT-PMP, so this
// implementation reuses the gateway address + retry pattern from
// natpmp.go.
//
// Wire format (RFC 6887 §7.1) — common header:
//
//   [VER:1] [R+OP:1] [RESERVED:1] [RESERVED:1] [LIFETIME:4] [CLIENT_IP:16]
//
// VER=2. R-bit (top bit of byte 1) = 0 for request, 1 for response.
// Opcode is the bottom 7 bits of byte 1.
//
// MAP opcode payload (§11.1, 36 bytes):
//
//   [NONCE:12] [PROTO:1] [RESERVED:3] [INTERNAL_PORT:2] [EXTERNAL_PORT:2] [EXTERNAL_IP:16]
//
// CLIENT_IP and EXTERNAL_IP are IPv6-formatted: IPv4 carried as
// IPv4-mapped IPv6 (::ffff:a.b.c.d). NONCE is a 12-byte random
// identifier the server echoes; we use it to correlate retries.
//
// Version negotiation: if the server speaks NAT-PMP only it replies
// with VER=0 RESULT_CODE=1 (UNSUPP_VERSION) — caller should fall
// back to NAT-PMP. We don't need explicit fallback because the
// coordinator probes both protocols in parallel.

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

const (
	pcpVersion       = 2
	pcpRequestRBit   = 0x00 // request: high bit clear
	pcpResponseRBit  = 0x80 // response: high bit set
	pcpOpcodeMASK    = 0x7f
	pcpOpcodeMAP     = 1
	pcpProtoUDP      = 17
	pcpHeaderLen     = 24
	pcpMapPayloadLen = 36
	pcpReqLen        = pcpHeaderLen + pcpMapPayloadLen
)

// PCP result codes (§7.4).
const (
	pcpRCSuccess           = 0
	pcpRCUnsupportedVersion = 1
	pcpRCNotAuthorized      = 2
	pcpRCMalformedRequest   = 3
)

// PCP is a stateless PCP-v2 client for a single gateway. Reuses the
// NATPMP roundtrip mechanics (same UDP port + retry strategy).
type PCP struct {
	Gateway netip.Addr
	natpmp  *NATPMP // for borrowing roundTrip's retry/backoff loop
}

func NewPCP(gateway netip.Addr) *PCP {
	return &PCP{
		Gateway: gateway,
		natpmp:  &NATPMP{Gateway: gateway},
	}
}

func (c *PCP) Name() string { return "pcp" }

// Map requests an external UDP port mapping. Returns the public
// AddrPort and the granted lifetime.
func (c *PCP) Map(ctx context.Context, internalPort uint16) (netip.AddrPort, time.Duration, error) {
	if !c.Gateway.IsValid() || !c.Gateway.Is4() {
		return netip.AddrPort{}, 0, fmt.Errorf("pcp: invalid IPv4 gateway %v", c.Gateway)
	}

	clientIP, err := localIPv4Toward(c.Gateway)
	if err != nil {
		return netip.AddrPort{}, 0, err
	}

	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return netip.AddrPort{}, 0, fmt.Errorf("pcp: random nonce: %w", err)
	}

	const lifetime = uint32(7200)
	req := buildPCPMapRequest(internalPort, clientIP, nonce, lifetime)

	resp, err := c.natpmp.roundTrip(ctx, req, pcpReqLen)
	if err != nil {
		return netip.AddrPort{}, 0, err
	}

	parsed, err := parsePCPMapResponse(resp, nonce)
	if err != nil {
		return netip.AddrPort{}, 0, err
	}

	return netip.AddrPortFrom(parsed.externalIP, parsed.externalPort),
		time.Duration(parsed.lifetime) * time.Second, nil
}

// Unmap removes the mapping by re-issuing MAP with lifetime=0
// (RFC 6887 §15.1). Best-effort.
func (c *PCP) Unmap(ctx context.Context, internalPort uint16) error {
	clientIP, err := localIPv4Toward(c.Gateway)
	if err != nil {
		return err
	}
	nonce := make([]byte, 12)
	_, _ = rand.Read(nonce)
	req := buildPCPMapRequest(internalPort, clientIP, nonce, 0)
	_, err = c.natpmp.roundTrip(ctx, req, pcpReqLen)
	return err
}

// pcpMapResponse is the parsed shape of a successful MAP reply.
type pcpMapResponse struct {
	externalIP   netip.Addr
	externalPort uint16
	lifetime     uint32
}

func buildPCPMapRequest(internalPort uint16, clientIPv4 netip.Addr, nonce []byte, lifetime uint32) []byte {
	req := make([]byte, pcpReqLen)
	req[0] = pcpVersion
	req[1] = pcpRequestRBit | pcpOpcodeMAP
	// bytes 2-3: reserved (zero)
	binary.BigEndian.PutUint32(req[4:8], lifetime)
	// bytes 8-23: client IP (IPv4-mapped IPv6)
	copy(req[8:24], ipv4MappedIPv6(clientIPv4))
	// MAP payload starts at byte 24.
	copy(req[24:36], nonce)
	req[36] = pcpProtoUDP
	// bytes 37-39: reserved
	binary.BigEndian.PutUint16(req[40:42], internalPort)
	binary.BigEndian.PutUint16(req[42:44], internalPort) // suggested external port
	// bytes 44-59: suggested external IP (zeros = "any")
	return req
}

func parsePCPMapResponse(resp, expectedNonce []byte) (*pcpMapResponse, error) {
	if len(resp) < pcpReqLen {
		return nil, fmt.Errorf("pcp: response too short (%d bytes, expected at least %d)", len(resp), pcpReqLen)
	}
	if resp[0] != pcpVersion {
		return nil, fmt.Errorf("pcp: unsupported version %d in response", resp[0])
	}
	if resp[1]&pcpResponseRBit == 0 {
		return nil, errors.New("pcp: response missing R bit")
	}
	if resp[1]&pcpOpcodeMASK != pcpOpcodeMAP {
		return nil, fmt.Errorf("pcp: unexpected opcode 0x%02x in response", resp[1])
	}
	if rc := resp[3]; rc != pcpRCSuccess {
		return nil, pcpResultCodeError(rc)
	}
	lifetime := binary.BigEndian.Uint32(resp[4:8])
	// bytes 8-23: epoch + reserved we don't need

	// MAP-specific payload starts at byte 24.
	if !bytesEqual(resp[24:36], expectedNonce) {
		return nil, errors.New("pcp: response nonce does not match request")
	}
	if resp[36] != pcpProtoUDP {
		return nil, fmt.Errorf("pcp: response protocol %d != UDP", resp[36])
	}
	extPort := binary.BigEndian.Uint16(resp[42:44])
	if extPort == 0 {
		return nil, errors.New("pcp: router returned external port 0")
	}
	extIP, err := pcpExtractIPv4FromIPv6(resp[44:60])
	if err != nil {
		return nil, err
	}
	return &pcpMapResponse{
		externalIP:   extIP,
		externalPort: extPort,
		lifetime:     lifetime,
	}, nil
}

func pcpResultCodeError(rc byte) error {
	switch rc {
	case pcpRCUnsupportedVersion:
		return fmt.Errorf("pcp: unsupported version (rc=1) — gateway speaks NAT-PMP, not PCP")
	case pcpRCNotAuthorized:
		return fmt.Errorf("pcp: not authorized (rc=2)")
	case pcpRCMalformedRequest:
		return fmt.Errorf("pcp: malformed request (rc=3)")
	default:
		return fmt.Errorf("pcp: result code %d", rc)
	}
}

// ipv4MappedIPv6 returns the 16-byte IPv4-mapped IPv6 representation
// of an IPv4 address: ::ffff:a.b.c.d.
func ipv4MappedIPv6(addr netip.Addr) []byte {
	out := make([]byte, 16)
	out[10] = 0xff
	out[11] = 0xff
	if addr.Is4() {
		v4 := addr.As4()
		copy(out[12:16], v4[:])
	}
	return out
}

// pcpExtractIPv4FromIPv6 reads a 16-byte buffer in IPv4-mapped IPv6
// form and returns the embedded IPv4 address.
func pcpExtractIPv4FromIPv6(b []byte) (netip.Addr, error) {
	if len(b) != 16 {
		return netip.Addr{}, fmt.Errorf("pcp: expected 16-byte IP, got %d", len(b))
	}
	// Verify ::ffff:0:0/96 prefix (IPv4-mapped).
	for i := 0; i < 10; i++ {
		if b[i] != 0 {
			return netip.Addr{}, fmt.Errorf("pcp: external IP not IPv4-mapped (byte %d = 0x%02x)", i, b[i])
		}
	}
	if b[10] != 0xff || b[11] != 0xff {
		return netip.Addr{}, errors.New("pcp: external IP not IPv4-mapped (missing 0xffff)")
	}
	var v4 [4]byte
	copy(v4[:], b[12:16])
	addr := netip.AddrFrom4(v4)
	if addr.IsUnspecified() {
		return netip.Addr{}, errors.New("pcp: router reported 0.0.0.0 as external IP (likely double-NAT)")
	}
	return addr, nil
}

// localIPv4Toward returns the local IPv4 the kernel would use to reach
// the given gateway. Used as the CLIENT_IP field in PCP requests.
func localIPv4Toward(gateway netip.Addr) (netip.Addr, error) {
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{
		IP:   gateway.AsSlice(),
		Port: 1, // any port — we never write
	})
	if err != nil {
		return netip.Addr{}, fmt.Errorf("pcp: probe local IP toward %s: %w", gateway, err)
	}
	defer conn.Close()
	la := conn.LocalAddr().(*net.UDPAddr)
	addr, ok := netip.AddrFromSlice(la.IP.To4())
	if !ok {
		return netip.Addr{}, fmt.Errorf("pcp: cannot determine local IPv4")
	}
	return addr, nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
