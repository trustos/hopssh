package portmap

// SSDP (Simple Service Discovery Protocol) — UPnP Forum Device
// Architecture v1.0 §1.2. We send M-SEARCH multicast on
// 239.255.255.250:1900 and read HTTP-shaped UDP replies advertising
// services. Each reply carries a LOCATION header that points at the
// device description XML.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"strings"
	"time"
)

const (
	ssdpMulticastAddr = "239.255.255.250:1900"
	ssdpMaxWaitSec    = 2
)

// IGD search target strings to try in order. v2 first (more capable
// services), then v1 (more universal coverage).
var ssdpSearchTargets = []string{
	"urn:schemas-upnp-org:device:InternetGatewayDevice:2",
	"urn:schemas-upnp-org:device:InternetGatewayDevice:1",
}

// ssdpReply is one parsed M-SEARCH response from an IGD.
type ssdpReply struct {
	// Location is the device description XML URL (the LOCATION header).
	Location string
	// ST is the search target the device matched on (the ST header).
	ST string
	// Server is the server-identifier string from the device (SERVER
	// header). Used for vendor-specific bug heuristics later.
	Server string
}

// discoverIGD sends SSDP M-SEARCH and returns each unique IGD reply
// observed within the wait window. Empty on no replies. Errors only on
// a setup failure (cannot bind multicast socket); a quiet network is
// not an error.
func discoverIGD(ctx context.Context) ([]ssdpReply, error) {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, fmt.Errorf("ssdp: listen: %w", err)
	}
	defer conn.Close()

	dst, err := net.ResolveUDPAddr("udp4", ssdpMulticastAddr)
	if err != nil {
		return nil, fmt.Errorf("ssdp: resolve %s: %w", ssdpMulticastAddr, err)
	}

	for _, st := range ssdpSearchTargets {
		req := buildMSearch(st)
		if _, err := conn.WriteTo(req, dst); err != nil {
			// One ST failing isn't fatal — try the others.
			continue
		}
	}

	deadline := time.Now().Add(time.Duration(ssdpMaxWaitSec) * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetReadDeadline(deadline)

	replies := make(map[string]ssdpReply) // dedupe by Location
	buf := make([]byte, 4096)
	for {
		if err := ctx.Err(); err != nil {
			break
		}
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			break // deadline reached; not an error
		}
		r, ok := parseSSDPReply(buf[:n])
		if !ok || r.Location == "" {
			continue
		}
		if !isIGDST(r.ST) {
			continue
		}
		replies[r.Location] = r
	}

	out := make([]ssdpReply, 0, len(replies))
	for _, r := range replies {
		out = append(out, r)
	}
	return out, nil
}

func buildMSearch(st string) []byte {
	// CRLF-terminated lines, blank line at end. Per RFC §1.2.2.
	return []byte(strings.Join([]string{
		"M-SEARCH * HTTP/1.1",
		"HOST: " + ssdpMulticastAddr,
		`MAN: "ssdp:discover"`,
		fmt.Sprintf("MX: %d", ssdpMaxWaitSec),
		"ST: " + st,
		"", // end of headers
		"",
	}, "\r\n"))
}

// parseSSDPReply parses an HTTP-shaped UDP reply. Returns ok=false on
// malformed input; caller skips silently.
func parseSSDPReply(b []byte) (ssdpReply, bool) {
	tp := textproto.NewReader(bufio.NewReader(bytes.NewReader(b)))

	// Status line: HTTP/1.1 200 OK
	line, err := tp.ReadLine()
	if err != nil {
		return ssdpReply{}, false
	}
	if !strings.HasPrefix(line, "HTTP/") {
		return ssdpReply{}, false
	}

	hdr, err := tp.ReadMIMEHeader()
	if err != nil {
		return ssdpReply{}, false
	}
	r := ssdpReply{
		Location: hdr.Get("Location"),
		ST:       hdr.Get("St"), // textproto canonicalizes "ST"
		Server:   hdr.Get("Server"),
	}
	return r, true
}

// isIGDST returns true if the given ST header refers to an Internet
// Gateway Device service. Devices may also reply with sub-service ST
// values (WANIPConnection etc.) — we accept those too because they
// imply an IGD parent.
func isIGDST(st string) bool {
	if st == "" {
		return false
	}
	switch {
	case strings.Contains(st, "InternetGatewayDevice"):
		return true
	case strings.Contains(st, "WANIPConnection"):
		return true
	case strings.Contains(st, "WANPPPConnection"):
		return true
	}
	return false
}

// errNoIGD is returned to the caller when SSDP completed without
// observing any IGD replies.
var errNoIGD = errors.New("ssdp: no IGD found")
