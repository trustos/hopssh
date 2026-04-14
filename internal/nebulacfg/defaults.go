// Package nebulacfg defines shared Nebula configuration defaults used by
// both the agent (config generation at enrollment) and the server (config
// refresh via cert renewal). Centralizing these ensures consistency and
// makes it easy to tune networking behavior in one place.
package nebulacfg

// PreferredRanges tells Nebula to prefer these IP ranges when choosing
// which endpoint to use for a peer. RFC 1918 private ranges enable
// direct LAN connections between peers on the same local network.
var PreferredRanges = []string{
	"192.168.0.0/16",
	"10.0.0.0/8",
	"172.16.0.0/12",
}

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

// TunMTU is the MTU for the Nebula TUN interface in kernel mode.
// 1400 balances encapsulation overhead (Nebula adds ~60 bytes) with
// good throughput. The default 1300 is too conservative for most networks.
const TunMTU = 1400

// PreferredRangesYAML returns the preferred_ranges block as YAML for
// embedding in Nebula config templates.
func PreferredRangesYAML() string {
	s := "preferred_ranges:\n"
	for _, r := range PreferredRanges {
		s += "  - " + r + "\n"
	}
	return s
}
