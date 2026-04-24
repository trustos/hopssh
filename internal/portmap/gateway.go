package portmap

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// DiscoverGateway returns the default IPv4 gateway (first-hop router) for
// the local machine. Used by NAT-PMP and PCP clients to address the local
// router; UPnP-IGD uses SSDP multicast and doesn't need the gateway IP.
func DiscoverGateway() (netip.Addr, error) {
	switch runtime.GOOS {
	case "darwin":
		return discoverGatewayDarwin()
	case "linux":
		return discoverGatewayLinux()
	case "windows":
		return discoverGatewayWindows()
	default:
		return netip.Addr{}, fmt.Errorf("portmap: unsupported OS %q", runtime.GOOS)
	}
}

// macOS: parse `route -n get default`.
//
// Example output:
//
//	   route to: default
//	destination: default
//	       mask: default
//	    gateway: 192.168.0.1
//	  interface: en0
//	      flags: <UP,GATEWAY,DONE,STATIC,PRCLONING,GLOBAL>
//	 recvpipe  sendpipe  ssthresh  rtt,msec    rttvar  hopcount      mtu     expire
//	       0         0         0         0         0         0      1500         0
var darwinGatewayRE = regexp.MustCompile(`(?m)^\s*gateway:\s*(\S+)`)

func discoverGatewayDarwin() (netip.Addr, error) {
	out, err := exec.Command("route", "-n", "get", "default").Output()
	if err != nil {
		return netip.Addr{}, fmt.Errorf("portmap: route command: %w", err)
	}
	m := darwinGatewayRE.FindSubmatch(out)
	if len(m) < 2 {
		return netip.Addr{}, fmt.Errorf("portmap: gateway line not found in `route` output")
	}
	return parseGatewayString(string(m[1]))
}

// Linux: parse /proc/net/route.
//
// The file is a table with a one-line header. The default route has
// destination `00000000` (0.0.0.0 in little-endian hex). The `Gateway`
// column is also little-endian hex.
//
// Example:
//
//	Iface   Destination Gateway     Flags   RefCnt Use Metric Mask        MTU Window  IRTT
//	eth0    00000000    0100A8C0    0003    0      0   100    00000000    0   0       0
//
// Where 0100A8C0 → bytes 01, 00, A8, C0 → IP 192.168.0.1 (reversed).
func discoverGatewayLinux() (netip.Addr, error) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return netip.Addr{}, fmt.Errorf("portmap: open /proc/net/route: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Scan() // skip header
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 8 {
			continue
		}
		// Destination must be 00000000 for default route.
		if fields[1] != "00000000" {
			continue
		}
		gwHex := fields[2]
		raw, err := hex.DecodeString(gwHex)
		if err != nil || len(raw) != 4 {
			continue
		}
		// /proc/net/route gives little-endian; flip to big-endian.
		var ip4 [4]byte
		ip4[0] = raw[3]
		ip4[1] = raw[2]
		ip4[2] = raw[1]
		ip4[3] = raw[0]
		addr := netip.AddrFrom4(ip4)
		if addr.IsUnspecified() {
			continue
		}
		return addr, nil
	}
	if err := sc.Err(); err != nil {
		return netip.Addr{}, fmt.Errorf("portmap: scan /proc/net/route: %w", err)
	}
	return netip.Addr{}, fmt.Errorf("portmap: no default route in /proc/net/route")
}

// Windows: parse `route print -4 0.0.0.0`.
//
// Example line in the "Active Routes" section:
//
//	  0.0.0.0          0.0.0.0     192.168.1.1     192.168.1.100      25
//
// We pick the first row whose network destination and mask are both 0.0.0.0.
var windowsGatewayRE = regexp.MustCompile(`(?m)^\s*0\.0\.0\.0\s+0\.0\.0\.0\s+(\S+)\s+`)

func discoverGatewayWindows() (netip.Addr, error) {
	out, err := exec.Command("route", "print", "-4", "0.0.0.0").Output()
	if err != nil {
		return netip.Addr{}, fmt.Errorf("portmap: route print: %w", err)
	}
	m := windowsGatewayRE.FindSubmatch(out)
	if len(m) < 2 {
		return netip.Addr{}, fmt.Errorf("portmap: default route not found in `route print` output")
	}
	return parseGatewayString(string(m[1]))
}

func parseGatewayString(s string) (netip.Addr, error) {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("portmap: parse gateway %q: %w", s, err)
	}
	if !addr.Is4() {
		return netip.Addr{}, fmt.Errorf("portmap: gateway %q is not IPv4", s)
	}
	if addr.IsUnspecified() || addr.IsLoopback() {
		return netip.Addr{}, fmt.Errorf("portmap: gateway %q is not routable", s)
	}
	return addr, nil
}

// parseLinuxGatewayHex parses a little-endian hex gateway address as
// written in /proc/net/route. Exposed to tests.
func parseLinuxGatewayHex(hexStr string) (netip.Addr, error) {
	raw, err := hex.DecodeString(hexStr)
	if err != nil {
		return netip.Addr{}, err
	}
	if len(raw) != 4 {
		return netip.Addr{}, fmt.Errorf("expected 4 bytes, got %d", len(raw))
	}
	var ip4 [4]byte
	for i := 0; i < 4; i++ {
		ip4[i] = raw[3-i]
	}
	return netip.AddrFrom4(ip4), nil
}
