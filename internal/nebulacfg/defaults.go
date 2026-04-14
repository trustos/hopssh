// Package nebulacfg defines shared Nebula configuration defaults used by
// both the agent (config generation at enrollment) and the server (config
// refresh via cert renewal). Centralizing these ensures consistency and
// makes it easy to tune networking behavior in one place.
package nebulacfg

import (
	"fmt"
	"net"
)

// UseRelays controls whether agents can relay through the lighthouse when
// direct P2P hole punching fails. Must be true — with false, Nebula skips
// relay entirely and connections fail behind strict firewalls or symmetric NAT.
// P2P is still preferred when hole punching succeeds (controlled by punchy settings).
const UseRelays = true

// PunchBack enables responsive hole punching: when a peer tries to punch
// through to us, we punch back. Improves NAT traversal success rate.
const PunchBack = true

// PunchDelay is the initial delay before sending punch packets. Gives
// the lighthouse time to share peer endpoint information before punching.
const PunchDelay = "1s"

// ListenPort is the default UDP port for the Nebula agent. A fixed port
// (vs port 0 / random) is critical for NAT hole punching — it keeps the
// NAT mapping stable across restarts and makes the port predictable for
// peers. ZeroTier uses 9993 for the same reason.
const ListenPort = 4242

// TunMTU is the MTU for the Nebula TUN interface in kernel mode.
// 1400 balances encapsulation overhead (Nebula adds ~60 bytes) with
// good throughput. The default 1300 is too conservative for most networks.
const TunMTU = 1400

// DetectPhysicalSubnet discovers the local IP that routes to a given remote
// host and returns its subnet as a CIDR string. This identifies the real
// physical network interface — not overlay/VPN interfaces — because the OS
// routing table selects the interface that reaches the public internet.
//
// For example, if the lighthouse is at 132.145.232.64 and this machine has:
//   - 192.168.23.3 on en0 (WiFi)        ← routes to the internet
//   - 10.147.20.193 on feth3857 (ZeroTier)
//   - 10.42.1.7 on utun10 (Nebula)
//
// This function returns "192.168.23.0/24" — the real LAN subnet.
// Nebula then only advertises this subnet as an endpoint, preventing
// double-tunneling through other overlays.
//
// Tries IPv4 first, falls back to IPv6 for IPv6-only underlay networks.
func DetectPhysicalSubnet(remoteHost string) (string, error) {
	// Try IPv4 first (most common case).
	subnet, err := detectSubnetForNetwork("udp4", remoteHost)
	if err == nil {
		return subnet, nil
	}

	// Fall back to IPv6 for IPv6-only underlay networks.
	subnet, err6 := detectSubnetForNetwork("udp6", remoteHost)
	if err6 == nil {
		return subnet, nil
	}

	return "", fmt.Errorf("detect physical subnet: ipv4: %w, ipv6: %v", err, err6)
}

func detectSubnetForNetwork(network, remoteHost string) (string, error) {
	// UDP dial doesn't actually send packets — it just asks the OS
	// which local IP would be used to reach this destination.
	conn, err := net.Dial(network, net.JoinHostPort(remoteHost, "1"))
	if err != nil {
		return "", err
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	localIP := localAddr.IP

	// Find the interface and subnet mask for this IP.
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("list interfaces: %w", err)
	}

	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			if ipNet.IP.Equal(localIP) {
				networkAddr := ipNet.IP.Mask(ipNet.Mask)
				ones, _ := ipNet.Mask.Size()
				return fmt.Sprintf("%s/%d", networkAddr, ones), nil
			}
		}
	}

	// Fallback: use /24 (IPv4) or /64 (IPv6) around the detected IP.
	if localIP.To4() != nil {
		masked := localIP.Mask(net.CIDRMask(24, 32))
		return fmt.Sprintf("%s/24", masked), nil
	}
	masked := localIP.Mask(net.CIDRMask(64, 128))
	return fmt.Sprintf("%s/64", masked), nil
}

// LocalAllowListYAML returns the local_allow_list YAML block that restricts
// Nebula to only advertise endpoints on the given subnet. Pass the result
// of DetectPhysicalSubnet(). If subnet is empty, returns empty string (no filter).
func LocalAllowListYAML(physicalSubnet string) string {
	if physicalSubnet == "" {
		return ""
	}
	return fmt.Sprintf("  local_allow_list:\n    \"%s\": true\n", physicalSubnet)
}
