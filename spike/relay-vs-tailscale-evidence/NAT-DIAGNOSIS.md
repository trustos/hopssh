# NAT Traversal Diagnosis — why Tailscale goes direct and hopssh relays

Date: 2026-04-20
Topology: MacBook Pro (cellular, Yettel BG CGNAT) ↔ Mac mini (home Ethernet, TP-Link router, Bulgaria)

## Conclusion upfront

**Tailscale uses NAT-PMP to ask the home router to forward a public port to the Mac mini. Nebula does not.** That single missing feature is the primary reason hopssh relays where Tailscale goes direct on this network pair.

We have no UPnP / NAT-PMP / PCP client anywhere in our codebase (vendor or agent). Tailscale's portmapper is ~1500 lines of Go that we'd need to implement equivalents of — custom, no external deps.

## Evidence

### 1. Tailscale live session state (Mac mini, 2026-04-20 12:38)

From `tailscale status --json`:

```
Self (Mac mini):
  Public endpoint: 46.10.240.91:41641   ← mapped via NAT-PMP
  Addrs: [
    "46.10.240.91:41641",   ← public (port-mapped)
    "10.42.1.7:41641",
    "192.168.23.3:41641",
    ...
  ]
  Relay (home DERP): fra (Frankfurt)

Peer (MacBook Pro, cellular):
  CurAddr: 149.62.207.240:24240   ← direct P2P endpoint
  Active: true
  LastHandshake: 2026-04-20T12:38:27
  RxBytes: 4.8 MB
  TxBytes: 52 MB
```

The MacBook Pro is currently sending Mac mini 52 MB over **direct** WireGuard (not through Frankfurt DERP). Path:
- MBP cellular (CGNAT 149.62.207.240:24240) ↔ Mac mini home router (46.10.240.91:41641)

### 2. Tailscale netcheck — Mac mini NAT behaviour

From `tailscale netcheck`:

```
UDP: true
IPv4: yes, 46.10.240.91:59724
MappingVariesByDestIP: false            ← endpoint-independent ("cone") NAT
PortMapping: UPnP, NAT-PMP              ← both protocols detected
CaptivePortal: false
Nearest DERP: Warsaw (73.5ms)
```

Key line from verbose log:
```
portmap: [v1] Got PMP response; IP: 46.10.240.91, epoch: 2699106
```

**This is the whole story in one line.** Tailscale's portmap subsystem sent a NAT-PMP request to the TP-Link router's default gateway (192.168.23.1). The router responded with a permanent (2-hour) public-port forwarding for Tailscale's listen port (41641). Tailscale then advertised `46.10.240.91:41641` to its peers via the control plane. The MacBook Pro received this endpoint via Tailscale's discovery and opened a WireGuard connection to it — succeeded on first attempt because the port is literally forwarded at the router layer.

### 3. Nebula vendor code has no port-mapping support

```
$ grep -ri "upnp\|nat-pmp\|pcp\|portmap" vendor/github.com/slackhq/nebula/*.go
(no matches)
```

`vendor/github.com/slackhq/nebula/punchy.go` (110 lines) is pure hole-punching:
- On `HostPunchNotification` from lighthouse, send a UDP packet to the peer's advertised endpoint.
- `PunchDelay: 100ms`, `RespondDelay: 500ms`, `target_all_remotes: true`.
- No interaction with the local router.

This design assumes both peers have reachable UDP to each other after a single punch — fine for symmetric cone-NAT pairs, broken for one-side-CGNAT pairs.

### 4. What happens in our current Nebula handshake (inferred)

Without port mapping, on the Mac-mini-at-home / MBP-on-cellular path:

1. Both agents register with the lighthouse. Each advertises their discovered endpoints (local LAN IPs + the source-IP:port the lighthouse sees them from).
2. Mac mini's advertised public endpoint is `46.10.240.91:<ephemeral>` where `<ephemeral>` is whatever source port the TP-Link chose for the lighthouse flow. The router has a mapping for THIS flow (lighthouse-bound), not for an arbitrary incoming packet.
3. Lighthouse notifies MBP: "Mac mini is at 46.10.240.91:4242 (or whatever Nebula's listen port is on Mac mini)."
4. MBP sends Noise handshake to `46.10.240.91:4242`. **TP-Link router has no mapping for port 4242** (no UPnP/NAT-PMP happened). Drop.
5. Simultaneously: Mac mini sends Noise handshake to MBP's cellular address. MBP's CGNAT may or may not have a matching outbound mapping. Usually not (MBP isn't expecting packets from this specific source).
6. Both attempts fail. Lighthouse-as-relay takes over.

With Tailscale:
- Step 2 is different — Mac mini's port 41641 was ALREADY forwarded via NAT-PMP at startup, independent of any peer.
- Step 4 succeeds on the first attempt because TP-Link has a permanent `46.10.240.91:41641 → 192.168.23.3:41641` mapping.
- Direct tunnel established.

## Why previous "symmetric NAT is unsolvable" claim in CLAUDE.md was wrong

The discovery log says: *"Symmetric NAT with random ports is unsolvable — port prediction (±50) doesn't work when the carrier assigns random ports. This affects most mobile carriers (CGNAT). No VPN can establish P2P through truly random symmetric NAT."*

Correct in the narrow sense: if BOTH sides are behind CGNAT, no VPN reliably punches through. **But the user's topology isn't that** — one side is behind a standard home router that supports UPnP/NAT-PMP. Tailscale wins that asymmetric case trivially by asking the router to open a hole; the cellular side then makes an outbound connection to that stable public endpoint, which ALL CGNAT variants allow.

The CLAUDE.md claim needs an update after the fix ships.

## Proposed fix

See `PORT-MAPPING-DESIGN.md` in this directory. Summary:

- New package `internal/portmap` implementing NAT-PMP (RFC 6886), UPnP-IGD (UPnP Forum spec), and PCP (RFC 6887). All three hand-rolled, zero external dependencies.
- New Nebula vendor patch (#11): on `main.Main` startup, try portmapping in background; if it succeeds, add the public `IP:port` to the endpoint list advertised to lighthouse; refresh before expiry.
- The patch is conditional on a new `portmap.enabled: true` config knob (default true) so users can disable it on networks where it causes problems.

Expected outcome on this specific network pair: MBP ↔ Mac mini goes to direct P2P, matching Tailscale. Verified via `hop-agent status` showing `relayed=false` and by reduced RTT on the TCP-RTT probe in the benchmark harness.

## Empirical validation (2026-04-20, during this session)

We ran a zero-code reachability test to validate the mechanism end-to-end before writing any Go.

### MBP netcheck (tethered via iPhone hotspot, Yettel BG cellular)

```
UDP: true
IPv4: yes, 149.62.207.240:32812
MappingVariesByDestIP: true              ← address-dependent (symmetric-variant) NAT
PortMapping: (empty)                     ← no UPnP, no NAT-PMP, no PCP
Nearest DERP: Amsterdam (63.5ms)
```

### Step B: raw UDP without UPnP mapping

- Mac mini: Python listener on UDP 4242 (also ran tcpdump on en0 later).
- MBP: `echo ... | nc -u 46.10.240.91 4242` × 8 probes.
- **Result: zero probes reached Mac mini.** TP-Link has no forwarding for port 4242 → drops at the router.
- Only traffic we saw on UDP 4242 was from the hopssh lighthouse `132.145.232.64:42001`, because a stale NAT mapping existed from when hop-agent had been running (cone NAT, lingering mapping, known to the lighthouse). Not accessible to MBP because MBP doesn't know the ephemeral external port the router picked.

### Step C: raw UDP WITH UPnP mapping

- Mac mini: `upnpc -a 192.168.23.3 4242 4242 UDP 600` — creates deterministic mapping `external 46.10.240.91:4242 → internal 192.168.23.3:4242`.
- MBP: same `nc -u` probes.
- **Result: tcpdump on Mac mini captured `IP 149.62.207.240.28382 > 192.168.23.3.4242: UDP, length 25`.** Multiple probes with varying source ports (28382, 32090, 33168 — confirms MBP's CGNAT assigns a fresh random source port per outbound flow, which is the classic symmetric-variant behaviour).
- The Python listener initially didn't show the packets because macOS Application Firewall was blocking inbound to user-space Python; tcpdump at BPF level captured them cleanly. For the actual Nebula agent, macOS ALF prompts to allow the binary on first run — not a blocker.

### What this proves

1. **UPnP on TP-Link works end-to-end.** Mapping created → packet from cellular MBP → ISP → TP-Link NAT applied → Mac mini NIC. Zero drops on the path when the mapping exists.
2. **The forward direction is the only one we need.** MBP source port varies per flow, so Mac-mini → MBP punching is unfixable from the Mac mini side (port unpredictable). But once MBP **initiates** a flow to Mac mini's stable public port, the cellular CGNAT creates an outbound mapping that holds for the lifetime of traffic, and Mac mini replies on that flow — works both ways without any tricks on the MBP side.
3. **Nebula's symmetric "both peers punch" strategy is wrong for this topology.** The fix is to make Mac mini's port publicly reachable via UPnP, then let MBP do the outbound.
4. **Tailscale is doing exactly this.** Live `tailscale status` confirms active direct tunnel via `46.10.240.91:41641` — the NAT-PMP-mapped port. Frankfurt DERP only bootstraps the discovery; steady-state bytes flow direct.

The mechanism is bulletproof. Implementation can start.
