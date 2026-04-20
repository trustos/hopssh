package portmap

// NAT-PMP client — RFC 6886.
// https://www.rfc-editor.org/rfc/rfc6886.html
//
// Wire format (all multi-byte fields network byte order):
//
//   External-address request  (2 bytes): [ver=0 op=0]
//   External-address response (12 bytes): [ver=0 op=0x80 result:2 epoch:4 pub_ipv4:4]
//
//   UDP MAP request  (12 bytes): [ver=0 op=1 rsvd:2 int_port:2 ext_port:2 lifetime:4]
//   UDP MAP response (16 bytes): [ver=0 op=0x81 result:2 epoch:4 int_port:2 ext_port:2 lifetime:4]
//
// Retry per RFC §3.1: start at 250 ms, double on each timeout, up to 9
// attempts (≈ 64 s) before declaring the server absent.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

const (
	natpmpPort    = 5351
	natpmpVersion = 0

	natpmpOpExternalAddr   = 0
	natpmpOpMapUDP         = 1
	natpmpResponseOpBit    = 0x80
	natpmpLifetimeRequest  = uint32(7200)
	natpmpLifetimeFallback = 60 * time.Minute
)

// NAT-PMP result codes (§3.5).
const (
	natpmpRCSuccess           = 0
	natpmpRCUnsupportedVersion = 1
	natpmpRCNotAuthorized      = 2
	natpmpRCNetworkFailure     = 3
	natpmpRCOutOfResources     = 4
	natpmpRCUnsupportedOpcode  = 5
)

// testOverrideDialPort is a test seam: tests set this to the loopback
// port their mock NAT-PMP server listens on. Zero in production.
var testOverrideDialPort uint16

// NATPMP is a stateless NAT-PMP client for a single gateway.
type NATPMP struct {
	Gateway netip.Addr
}

// NewNATPMP returns a client bound to the given gateway.
func NewNATPMP(gateway netip.Addr) *NATPMP { return &NATPMP{Gateway: gateway} }

func (c *NATPMP) Name() string { return "natpmp" }

// Map requests an external UDP port mapping. On success returns the full
// public AddrPort and the granted lifetime.
func (c *NATPMP) Map(ctx context.Context, internalPort uint16) (netip.AddrPort, time.Duration, error) {
	if !c.Gateway.IsValid() || !c.Gateway.Is4() {
		return netip.AddrPort{}, 0, fmt.Errorf("natpmp: invalid IPv4 gateway %v", c.Gateway)
	}

	// Query public address first. This also confirms the server speaks
	// NAT-PMP at all — if the gateway doesn't listen on UDP 5351 we fail
	// fast here before issuing the map request.
	pub, err := c.queryExternalAddr(ctx)
	if err != nil {
		return netip.AddrPort{}, 0, err
	}

	ext, ttl, err := c.requestMap(ctx, internalPort)
	if err != nil {
		return netip.AddrPort{}, 0, err
	}
	if ttl == 0 {
		ttl = natpmpLifetimeFallback
	}
	return netip.AddrPortFrom(pub, ext), ttl, nil
}

// Unmap removes a previously-requested mapping by sending a MAP request
// with lifetime=0 (RFC 6886 §3.3).
func (c *NATPMP) Unmap(ctx context.Context, internalPort uint16) error {
	req := make([]byte, 12)
	req[0] = natpmpVersion
	req[1] = natpmpOpMapUDP
	// bytes 2-3: reserved
	binary.BigEndian.PutUint16(req[4:6], internalPort)
	// ext_port=0, lifetime=0 → remove mapping
	_, err := c.roundTrip(ctx, req, 16)
	return err
}

func (c *NATPMP) queryExternalAddr(ctx context.Context) (netip.Addr, error) {
	req := []byte{natpmpVersion, natpmpOpExternalAddr}
	resp, err := c.roundTrip(ctx, req, 12)
	if err != nil {
		return netip.Addr{}, err
	}
	if resp[0] != natpmpVersion {
		return netip.Addr{}, fmt.Errorf("natpmp: unsupported version %d in response", resp[0])
	}
	if resp[1] != natpmpResponseOpBit|natpmpOpExternalAddr {
		return netip.Addr{}, fmt.Errorf("natpmp: unexpected opcode 0x%02x", resp[1])
	}
	if rc := binary.BigEndian.Uint16(resp[2:4]); rc != natpmpRCSuccess {
		return netip.Addr{}, resultCodeError("external-address", rc)
	}
	var ip4 [4]byte
	copy(ip4[:], resp[8:12])
	addr := netip.AddrFrom4(ip4)
	if addr.IsUnspecified() {
		return netip.Addr{}, errors.New("natpmp: router reported 0.0.0.0 as external IP (likely double-NAT)")
	}
	return addr, nil
}

func (c *NATPMP) requestMap(ctx context.Context, internalPort uint16) (ext uint16, ttl time.Duration, err error) {
	req := make([]byte, 12)
	req[0] = natpmpVersion
	req[1] = natpmpOpMapUDP
	// bytes 2-3: reserved
	binary.BigEndian.PutUint16(req[4:6], internalPort)
	// Suggest same external port for a deterministic mapping. Router MAY
	// assign a different one; we honor whatever it returns.
	binary.BigEndian.PutUint16(req[6:8], internalPort)
	binary.BigEndian.PutUint32(req[8:12], natpmpLifetimeRequest)

	resp, err := c.roundTrip(ctx, req, 16)
	if err != nil {
		return 0, 0, err
	}
	if resp[0] != natpmpVersion {
		return 0, 0, fmt.Errorf("natpmp: unsupported version %d in map response", resp[0])
	}
	if resp[1] != natpmpResponseOpBit|natpmpOpMapUDP {
		return 0, 0, fmt.Errorf("natpmp: unexpected map opcode 0x%02x", resp[1])
	}
	if rc := binary.BigEndian.Uint16(resp[2:4]); rc != natpmpRCSuccess {
		return 0, 0, resultCodeError("map", rc)
	}
	if got := binary.BigEndian.Uint16(resp[8:10]); got != internalPort {
		return 0, 0, fmt.Errorf("natpmp: response internal port %d does not match request %d", got, internalPort)
	}
	ext = binary.BigEndian.Uint16(resp[10:12])
	if ext == 0 {
		return 0, 0, errors.New("natpmp: router returned 0 as external port")
	}
	ttl = time.Duration(binary.BigEndian.Uint32(resp[12:16])) * time.Second
	return ext, ttl, nil
}

// roundTrip sends req to the gateway and reads one response. Implements
// the RFC §3.1 exponential backoff (250 ms → 500 ms → 1 s → ...) up to
// 9 attempts. Aborts immediately if ctx is cancelled or deadlines past.
func (c *NATPMP) roundTrip(ctx context.Context, req []byte, respLen int) ([]byte, error) {
	port := natpmpPort
	if testOverrideDialPort != 0 {
		port = int(testOverrideDialPort)
	}
	gwAddr := &net.UDPAddr{IP: c.Gateway.AsSlice(), Port: port}

	conn, err := net.DialUDP("udp4", nil, gwAddr)
	if err != nil {
		return nil, fmt.Errorf("natpmp: dial: %w", err)
	}
	defer conn.Close()

	const maxAttempts = 9
	backoff := 250 * time.Millisecond
	buf := make([]byte, respLen)

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		if _, err := conn.Write(req); err != nil {
			return nil, fmt.Errorf("natpmp: write: %w", err)
		}

		deadline := time.Now().Add(backoff)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		_ = conn.SetReadDeadline(deadline)

		n, _, err := conn.ReadFrom(buf)
		if err == nil && n >= respLen {
			return buf[:respLen], nil
		}

		backoff *= 2
	}
	return nil, fmt.Errorf("natpmp: no response from gateway %v after %d attempts", c.Gateway, maxAttempts)
}

func resultCodeError(op string, rc uint16) error {
	switch rc {
	case natpmpRCUnsupportedVersion:
		return fmt.Errorf("natpmp %s: unsupported version (rc=1) — gateway speaks a different protocol", op)
	case natpmpRCNotAuthorized:
		return fmt.Errorf("natpmp %s: not authorized (rc=2) — mapping disabled on gateway", op)
	case natpmpRCNetworkFailure:
		return fmt.Errorf("natpmp %s: network failure (rc=3)", op)
	case natpmpRCOutOfResources:
		return fmt.Errorf("natpmp %s: out of resources (rc=4) — gateway mapping table full", op)
	case natpmpRCUnsupportedOpcode:
		return fmt.Errorf("natpmp %s: unsupported opcode (rc=5)", op)
	default:
		return fmt.Errorf("natpmp %s: result code %d", op, rc)
	}
}
