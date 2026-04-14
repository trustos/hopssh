// Package nebulacfg defines shared Nebula configuration defaults used by
// both the agent (config generation at enrollment) and the server (config
// refresh via cert renewal). Centralizing these ensures consistency and
// makes it easy to tune networking behavior in one place.
package nebulacfg

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

// TunMTU is the MTU for the Nebula TUN interface in kernel mode.
// 1400 balances encapsulation overhead (Nebula adds ~60 bytes) with
// good throughput. The default 1300 is too conservative for most networks.
const TunMTU = 1400
