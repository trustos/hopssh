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
// History (chronological):
//
//  2026-04-21 — bisection on real cellular (Mac mini ↔ MacBook Pro,
//  Yettel BG, direct-P2P) chose 1420:
//
//    MTU  | DL Mb/s | UL Mb/s | retr DL | macOS HP screen-share
//    -----+---------+---------+---------+----------------------------
//    2800 |    7.8  |    1.5  |    191  | warning + retry → works
//    1440 |   42    |    6.8  |   1052  | starts cleanly, usable
//    1420 |   49    |    8.3  |    321  | warning + retry → works ✓ (was prod)
//    1380 |   41    |    6.8  |   1026  | login screen freezes (back then)
//    1280 |   52    |    9.0  |   9199  | black screen, no warning
//
//  2026-04-23 — re-bisection on home WiFi LAN (Mac mini Ethernet ↔
//  MacBook Pro WiFi) under different conditions, with the
//  v0.10.13+ stack (pipelined listenIn+listenOut, QOS_CLASS_USER_INTERACTIVE).
//  4 × 15 s iperf3 + 500-ping RTT per MTU, hopssh + Tailscale measured
//  in the same window:
//
//    MTU  | h.DL Mb | h.UL Mb | h.RTT mean | h.RTT std
//    -----+---------+---------+------------+----------
//    1280 |   300   |   211   |   29.3 ms  |  37.6 ms
//    1380 |   323   |   249   |   26.3 ms  |  34.3 ms  ← winner across DL, RTT mean, stddev
//    1420 |   192   |   264   |   27.8 ms  |  35.9 ms
//    1500 |   146   |   168   |   41.4 ms  |  67.1 ms  (1500+60 outer fragments)
//    2800 |   233   |   320   |   26.5 ms  |  39.9 ms  (22% packet loss)
//
//  3-way (hopssh@1380 vs Tailscale vs raw LAN) under the bad-WiFi
//  morning that followed: hopssh and Tailscale tied within noise on
//  every metric (DL 33 vs 37, UL 138 vs 139, RTT 30.9 vs 27.8 ms).
//  Manual screen-share test at 1380 confirmed working — the
//  "1380 tripped HP screen-share" failure mode from the original
//  cellular bisection did not reproduce on the v0.10.13+ codebase
//  (likely because patches 12/14/15/17/19 changed the heuristic
//  inputs avconferenced sees).
//
// Why 1380 wins now:
//   - Best aggregate WiFi LAN performer on the v0.10.13+ stack.
//   - Closer to upstream Nebula's `DefaultMTU = 1300` (60 bytes more
//     headroom than 1420 for outer-packet overhead on PPPoE / GRE /
//     IPSec underlay paths).
//   - Outer packet at 1380 inner = 1440 bytes (60-byte headers).
//     Fits inside 1500-byte standard Internet MTU with 60 bytes of
//     slack — comfortably more than 1420's 20-byte slack.
//   - macOS HP screen-share confirmed working at 1380 with the
//     current codebase.
//
// If you hit cellular path issues (high retransmits, 6 Mb/s plateau)
// you may want to lower to 1300 (Nebula default) which adds another
// 80 bytes of underlay headroom but loses ~10 % WiFi LAN throughput.
//
// The fundamental fix for HP independent of MTU is
// `NEPacketTunnelProvider` (Apple Dev ID, signed app, weeks of work)
// so avconferenced gets explicit VPN classification via the IS_VPN
// xflag + NetworkExtension agents instead of guessing from MTU.
const TunMTU = 1380

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
