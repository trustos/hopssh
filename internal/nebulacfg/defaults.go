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

// PortmapEnabled controls whether the agent tries UPnP/NAT-PMP/PCP at
// startup to obtain a public port mapping on the home router. Essential
// for direct P2P across asymmetric CGNAT (one peer on home router, one
// on cellular with no port-mapping). Disable only if the router
// misbehaves (sends bad UPnP responses, blacklists our mapping, etc.);
// can be overridden per-enrollment via nebula.yaml's `portmap.enabled`.
const PortmapEnabled = true

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

// TunMTU is the MTU for the Nebula TUN interface in kernel mode.
//
// 1420 — empirical sweet spot from a 2026-04-21 bisection on real
// cellular (Mac mini ↔ MacBook Pro, Yettel BG, direct-P2P verified):
//
//   MTU  | downlink | uplink  | retr DL | macOS HP screen-share
//   ------+----------+---------+---------+----------------------------
//   2800 | 7.8 Mb/s | 1.5 Mb/s|     191 | warning + retry → works
//   1440 |  42 Mb/s | 6.8 Mb/s|    1052 | starts cleanly, usable
//   1420 |  49 Mb/s | 8.3 Mb/s|     321 | warning + retry → works ✓
//   1380 |  41 Mb/s | 6.8 Mb/s|    1026 | login screen freezes
//   1280 |  52 Mb/s | 9.0 Mb/s|    9199 | black screen, no warning
//
// Why 1420 wins:
//   - Matches Tailscale's downlink throughput on the same cellular
//     path (~50 Mb/s) — 6.3× the 2800 baseline that was fragmenting.
//   - 28× fewer retransmits than 1280 (321 vs 9199) — closer to the
//     path's true capacity, less wasted air-time.
//   - Stays above the MTU heuristic that macOS HP Screen Sharing uses
//     on raw userspace utun (no IS_VPN flag). 1380 trips the heuristic
//     and HP fails (login screen freezes); 1420 stays clear of it and
//     HP works with the documented retry-once pattern.
//
// Outer-packet math: 1420 (utun) + 16 (Nebula header) + 16 (AEAD tag)
// + 8 (UDP header) + 20 (IP header) = 1480 bytes total IP packet.
// Fits inside 1500-byte standard Internet MTU with 20 bytes of slack
// for any carrier-side header overhead (PPPoE, GTP-U, etc.).
//
// The fundamental fix for HP independent of MTU is
// `NEPacketTunnelProvider` (Apple Dev ID, signed app, weeks of work)
// so avconferenced gets explicit VPN classification via the IS_VPN
// xflag + NetworkExtension agents instead of guessing from MTU.
// TunMTU final value — see comment block below for the bisection.
const TunMTU = 1420

// HandshakeTryInterval is the retry interval for Noise handshake attempts.
// Default 100ms wastes time if the lighthouse responds faster. 20ms ensures
// the handshake fires almost immediately after receiving peer addresses,
// reducing cold tunnel setup from 100-200ms to 40-80ms.
const HandshakeTryInterval = "20ms"

// Routines is the number of parallel TUN/UDP processing goroutines.
// Must be 1 — higher values create multiple SO_REUSEPORT sockets on
// macOS but Nebula falls back to 1 reader, leaving sockets unread.
const Routines = 1

// Cipher selects the Noise Protocol cipher. AES-GCM is the default because
// Apple Silicon and modern x86 have dedicated hardware AES instructions,
// making it faster than ChaCha20-Poly1305 (which uses NEON/SSE vector ops).
const Cipher = "aes"

// PreferredRangesYAML is the lighthouse preferred_ranges config block.
// Tells the lighthouse to prefer advertising local/private IPs when peers
// share the same public IP (same NAT). Without this, same-NAT peers spend
// 30-60s relaying through the lighthouse before P2P establishes via hairpin NAT.
const PreferredRangesYAML = `  preferred_ranges:
    - 192.168.0.0/16
    - 172.16.0.0/12
    - 10.0.0.0/8`


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
