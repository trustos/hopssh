# Sleep/Wake Resilience — Test Plan & Gap Analysis

*2026-04-17. Follows the sleep/wake fix in v0.9.6 (commit 606384b).*

---

## What we shipped (v0.9.6)

Two fixes for "Screen Sharing broken after overnight sleep":

1. **Tick-gap detection** (`cmd/agent/nebula.go:159`): if the 5s polling ticker
   fires and >15s have passed, force `RebindUDPServer()` + `CloseAllTunnels(true)`.
   Catches sleep/wake on the same WiFi where the address fingerprint doesn't change.

2. **Cert-expired cold-start** (`cmd/agent/renew.go:514-550`): when
   `reloadNebula()` finds `currentNebula == nil` (Nebula failed at boot due to
   expired cert), it starts Nebula fresh with `startMesh()` instead of returning
   "no embedded Nebula instance to reload."

Both validated empirically on macOS (Mac mini ↔ MacBook Pro, same LAN):
- Bug 2: 20s lid-close → rebind fired on wake → tunnel recovered in seconds.
- Bug 1: corrupted cert → OS stack → 5 min renewal → `"Nebula started after cert renewal"` → pings flowing.

---

## Why sleep/wake is hard (industry research)

Sleep simultaneously invalidates multiple network layers while the VPN process
is frozen by the OS. On wake, everything is stale at once:

- **NAT mappings expired.** Consumer routers use 30-120s UDP timeouts. Any sleep
  longer than this means packets sent to the old mapping are silently dropped.
- **WiFi reassociation.** Radio powers down, may rejoin a different SSID/subnet.
- **DNS resolver stale.** If the VPN provides DNS (split-DNS for mesh domains),
  the stub is dead during sleep. First queries hang. Tailscale's worst bug
  (#17736): `"dns udp query: request queue full"` — poisons entire system DNS.
- **Routing table changes.** systemd 253+ deletes WireGuard policy rules on
  suspend. macOS System Configuration may not reflect new state immediately.
- **Crypto keys/certs expired.** Our 24h Nebula certs are uniquely vulnerable.
- **Recovery ordering.** VPN can't rebind until WiFi reassociates. Can't resolve
  DNS until its own stub works. Can't verify tunnel until NAT mapping exists.
  The cascade can take 30-120s if any step stalls.

### What competitors do

| Competitor | Detection mechanism | Primary open issues |
|---|---|---|
| **Tailscale** | `netmon.Monitor` with `TimeJumped` (wall-clock gap — same heuristic as our tick-gap) + `NEProvider.sleep()`/`.wake()` on iOS/macOS NetworkExtension | #10688 (Linux, still open), #17736 (macOS DNS poisoning), #1554 (battery regression from aggressive wake handling) |
| **WireGuard** | None — relies on persistent keepalive timer (25s). Recovery = 1 keepalive interval + handshake RTT (~5-25s). | systemd 253 deletes routing rules on suspend (external breakage) |
| **ZeroTier** | None documented — waits for protocol-level peer re-discovery via roots. | 1-10+ minute recovery (#2026, #2545). Sometimes requires reboot. |
| **NetBird** | Added macOS sleep detection (v0.66.3, March 2026), but policy bugs (auto-reconnects after intentional disconnect). | #2454, #632, #3880 — actively iterating. |

**Key insight:** Tailscale's `TimeJumped` is equivalent to our tick-gap. They've
had it for years. Their open issues are edge cases that slip through despite
having this detection — DNS stub poisoning, Power Nap, battery tradeoffs.

---

## Failure mode matrix

| # | Failure mode | hopssh status | Priority |
|---|---|---|---|
| 1 | Stale UDP sockets (NAT expired) | ✅ **Handled** — tick-gap → rebind | — |
| 2 | Cert expired during sleep | ✅ **Handled** — cold-start in `reloadNebula()` | — |
| 3 | Address fingerprint change on wake | ✅ **Handled** — existing `watchNetworkChanges` | — |
| 4 | **DNS stub poisoning** — split-DNS resolver unreachable during recovery window, queries hang | ⚠️ **UNKNOWN — must test** | **HIGH** |
| 5 | Power Nap / dark wake (process runs, WiFi may be unavailable) | ❌ Not handled (tick-gap won't fire — process isn't suspended) | LOW (macOS edge case) |
| 6 | Very short sleep (<15s) | ⚠️ Tick-gap won't fire, but NAT survives. Natural recovery via keepalive. | LOW |
| 7 | systemd routing rule deletion (Linux) | ✅ Not vulnerable (Nebula uses TUN, not policy rules) | — |
| 8 | Wake to different WiFi (different SSID/subnet) | ✅ **Handled** — fingerprint changes → rebind | — |
| 9 | Dock/undock (WiFi ↔ Ethernet) | ✅ **Handled** — fingerprint includes all interfaces | — |
| 10 | Peer doesn't know we're gone (sends to old NAT) | ⚠️ Partially — peer's Nebula test interval (~15s) eventually triggers rehandshake. Window of black-hole sends. | MEDIUM |
| 11 | Recovery ordering (rebind before WiFi reassociates) | ⚠️ Partially — we rebind immediately; if WiFi isn't up, bind may fail. But `CloseAllTunnels(true)` forces rehandshake which retries. | MEDIUM |
| 12 | TUN device corruption after hibernation | ❌ Unknown — utun created at startup, not recreated after wake. Cert-expired cold-start path recreates (new Nebula), but only if cert expired. | LOW |
| 13 | Battery vs speed tradeoff | ❌ Not considered — rebind on every tick-gap unconditionally. Matters for mobile. | LOW (no mobile clients) |

---

## Test plan (executable in ~20 min, both Macs required)

### Prerequisites

- Both agents running v0.9.6+ (`hop-agent version`)
- Overlay tunnel confirmed: `ping -c 3 10.42.1.7` from MacBook succeeds
- Mac mini continuous ping running: `ping -i 1 10.42.1.6 | while IFS= read -r line; do echo "$(date +%H:%M:%S) $line"; done | tee /tmp/sleep-wake-ping.log`
- Mac mini agent log tail: `sudo tail -f /var/log/hop-agent.log | grep --line-buffered -E "Handshake|network change|sleep/wake|tunnel"`

### T1 — Short sleep (10s), same WiFi

**Goal:** verify NAT survives short sleep, no rebind needed.

```bash
# MacBook: confirm tunnel works, close lid for 10s, open, check
ping -c 2 10.42.1.7    # baseline
# close lid, count to 10, open
ping -c 5 10.42.1.7    # should work immediately
sudo grep "sleep/wake\|network change" /var/log/hop-agent.log | tail -3
```

**Expected:** pings resume immediately. Tick-gap may NOT fire (10s < 15s threshold).
NAT mapping survives. No rebind needed.

### T2 — Medium sleep (2 min), same WiFi

**Goal:** verify tick-gap fires, tunnel recovers after NAT expiry.

```bash
# MacBook: close lid for 2 minutes, open
date "+pre-sleep: %H:%M:%S"
# CLOSE LID — wait 2 minutes — OPEN LID
date "+post-wake: %H:%M:%S"
sudo grep "sleep/wake\|network change" /var/log/hop-agent.log | tail -5
ping -c 5 10.42.1.7
```

**Expected:** tick-gap fires (120s >> 15s threshold). Rebind + `CloseAllTunnels`.
Pings resume within 3-10s of wake. Mac mini's continuous ping shows a ~2 min gap
then recovery.

### T3 — Long sleep (10 min), same WiFi

**Goal:** deep recovery. Cert still valid (24h). Tests whether anything degrades
with longer sleep.

Same as T2 but 10 minutes. Check for any log anomalies (repeated handshake
failures, DNS issues).

### T4 — DNS during recovery window

**Goal:** validate split-DNS behavior during the 3-5s tunnel recovery after wake.

```bash
# MacBook: after waking from T2 or T3, IMMEDIATELY run:
dig +short $(hostname).home    # mesh hostname via split-DNS
dig +short google.com          # public DNS (should always work)
```

**Expected:** `google.com` resolves immediately (not routed through mesh DNS).
`hostname.home` may fail for 3-5s until tunnel recovers, then succeeds.
If `.home` queries hang for >10s or block public DNS, that's the DNS stub
poisoning bug (Tailscale's #17736 equivalent). **This is the most important
test.**

### T5 — Peer-side visibility during sleep

**Goal:** observe how long the Mac mini takes to notice the MacBook is gone
and re-discover it after wake.

Captured by the continuous ping running on Mac mini (`/tmp/sleep-wake-ping.log`).
Count consecutive `Request timeout` lines = outage duration as seen by peer.
First successful ping after the gap = tunnel recovery time from peer's perspective.

---

## After testing: decision matrix

| T4 result (DNS) | T2 result (recovery time) | Action |
|---|---|---|
| `.home` resolves within 5s, public DNS unaffected | Recovery <10s | **Ship as-is.** Document as "tunnels recover automatically after sleep." |
| `.home` hangs >10s but public DNS works | Recovery <10s | **Low priority fix.** DNS stub isn't poisoning the system, just slow for mesh names. Acceptable UX — user retries in a few seconds. |
| `.home` AND public DNS both hang | Any | **Must fix.** DNS stub is poisoning system DNS. Same class as Tailscale #17736. Fix: temporarily remove split-DNS config before sleep, re-add after tunnel recovers. |
| Any | Recovery >30s | **Investigate.** Check logs for what's stalling (WiFi reassociation? Handshake timeout? Lighthouse unreachable?). May need faster retry or OS-level wake notification. |

---

## What NOT to build (until evidence demands it)

- **OS-level sleep notifications** (`NSWorkspace.didWakeNotification`, `PrepareForSleep`).
  Our tick-gap heuristic is equivalent to Tailscale's `TimeJumped` which is their
  primary detection. OS notifications are a refinement for edge cases (Power Nap),
  not the foundation.
- **Power Nap handling.** macOS-specific edge case where the process runs during
  dark wake but WiFi may be unavailable. Only matters if users report it.
- **Battery optimization for wake handling.** Only relevant for iOS/Android
  mobile clients which we don't ship yet.
- **TUN device recreation after hibernation.** Only if testing T3 reveals utun
  corruption. macOS preserves utun across normal sleep.

---

## References

- Tailscale `net/netmon` package: `TimeJumped` heuristic, `ChangeDelta` struct
- Tailscale issues: [#1134](https://github.com/tailscale/tailscale/issues/1134),
  [#10688](https://github.com/tailscale/tailscale/issues/10688),
  [#17736](https://github.com/tailscale/tailscale/issues/17736),
  [#1554](https://github.com/tailscale/tailscale/issues/1554)
- ZeroTier issues: [#2026](https://github.com/zerotier/zerotierone/issues/2026),
  [#2545](https://github.com/zerotier/zerotierone/issues/2545)
- NetBird issues: [#2454](https://github.com/netbirdio/netbird/issues/2454),
  [#4892](https://github.com/netbirdio/netbird/issues/4892)
- hopssh fixes: commit 606384b (v0.9.6), `cmd/agent/nebula.go:147-185`,
  `cmd/agent/renew.go:514-560`
- Evidence: `spike/nebula-baseline-evidence/` (30s outage survival test)
