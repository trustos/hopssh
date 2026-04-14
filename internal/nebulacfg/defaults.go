// Package nebulacfg defines shared Nebula configuration defaults used by
// both the agent (config generation at enrollment) and the server (config
// refresh via cert renewal). Centralizing these ensures consistency and
// makes it easy to tune networking behavior in one place.
package nebulacfg

import (
	"fmt"
	"net"
	"regexp"
)

// UseRelays controls whether agents can relay through the lighthouse when
// direct P2P hole punching fails. Must be true — with false, Nebula skips
// relay entirely and connections fail behind strict firewalls or symmetric NAT.
// P2P is still preferred when hole punching succeeds (controlled by punchy settings).
const UseRelays = true

// PunchBack enables responsive hole punching: when a peer tries to punch
// through to us, we punch back. Improves NAT traversal success rate.
const PunchBack = true

// PunchDelay is the delay before sending punch packets after receiving a
// HostPunchNotification from the lighthouse. A short delay (100ms) ensures
// NAT mappings are created BEFORE the relay handshake completes (~170ms),
// giving direct P2P a chance to win the initial connection race.
const PunchDelay = "100ms"

// RespondDelay is the delay before sending a test packet back to a peer
// that queried the lighthouse about us. This triggers roaming detection
// on the initiator side. Default 5s is too slow — 500ms allows the
// initiator to detect the direct path within the first second.
const RespondDelay = "500ms"

// ListenPort is the default UDP port for the Nebula agent. A fixed port
// (vs port 0 / random) is critical for NAT hole punching — it keeps the
// NAT mapping stable across restarts and makes the port predictable for
// peers. ZeroTier uses 9993 for the same reason.
const ListenPort = 4242

// TunMTU is the safe default MTU for the Nebula TUN interface.
// 1440 = 1500 (Ethernet) - 60 (Nebula overhead: 16 header + 16 AEAD + 20 IP + 8 UDP).
// Zero IP fragmentation at this value. PMTUD (RFC 8899) automatically
// raises the MTU at runtime when the path supports larger packets.
const TunMTU = 1440

// HandshakeTryInterval is the retry interval for Noise handshake attempts.
// Default 100ms wastes time if the lighthouse responds faster. 20ms ensures
// the handshake fires almost immediately after receiving peer addresses,
// reducing cold tunnel setup from 100-200ms to 40-80ms.
const HandshakeTryInterval = "20ms"

// Routines is the number of parallel TUN/UDP processing goroutines.
// On macOS: 4 UDP readers via SO_REUSEPORT, 1 TUN reader (decoupled).
// On Linux: 4 parallel readers for both UDP and TUN (multiqueue).
const Routines = 4

// Cipher selects the Noise Protocol cipher. AES-GCM is the default because
// Apple Silicon and modern x86 have dedicated hardware AES instructions,
// making it faster than ChaCha20-Poly1305 (which uses NEON/SSE vector ops).
const Cipher = "aes"


// DetectPhysicalInterface discovers the OS network interface that routes to
// the given remote host. This identifies the real physical interface (WiFi,
// Ethernet) — not overlay/VPN interfaces — because the OS routing table
// selects the interface that reaches the public internet.
//
// Returns the interface name (e.g., "en0" on macOS, "eth0" on Linux,
// "Wi-Fi" on Windows). Used with Nebula's local_allow_list to restrict
// endpoint advertisement to only the physical interface.
//
// Tries IPv4 first, falls back to IPv6 for IPv6-only underlay networks.
func DetectPhysicalInterface(remoteHost string) (string, error) {
	name, err := detectInterfaceForNetwork("udp4", remoteHost)
	if err == nil {
		return name, nil
	}
	name, err6 := detectInterfaceForNetwork("udp6", remoteHost)
	if err6 == nil {
		return name, nil
	}
	return "", fmt.Errorf("detect physical interface: ipv4: %w, ipv6: %v", err, err6)
}

func detectInterfaceForNetwork(network, remoteHost string) (string, error) {
	conn, err := net.Dial(network, net.JoinHostPort(remoteHost, "1"))
	if err != nil {
		return "", err
	}
	defer conn.Close()

	localIP := conn.LocalAddr().(*net.UDPAddr).IP

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
				return iface.Name, nil
			}
		}
	}

	return "", fmt.Errorf("no interface found for IP %s", localIP)
}

// LocalAllowListYAML returns the local_allow_list YAML block that restricts
// Nebula to only advertise endpoints from the given interface. The interface
// name is regex-escaped for safety (Windows names may contain special chars).
// If interfaceName is empty, returns empty string (no filter).
func LocalAllowListYAML(interfaceName string) string {
	if interfaceName == "" {
		return ""
	}
	escaped := regexp.QuoteMeta(interfaceName)
	return fmt.Sprintf("  local_allow_list:\n    interfaces:\n      \"%s\": true\n", escaped)
}
