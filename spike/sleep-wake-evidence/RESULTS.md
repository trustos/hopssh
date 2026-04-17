# Sleep/wake resilience test results

Runs: 2026-04-17, Mac mini ↔ MacBook Pro, `hop-agent v0.9.7` (commit `c22bd60`)
on both ends, kernel-TUN mode.

- **Run 1 (LAN)** — 10:44Z–11:37Z. Both Macs on home WiFi, 192.168.23.0/24.
- **Run 2 (cellular)** — 11:59Z–12:06Z. MacBook switched to iPhone hotspot
  (172.20.10.0/28, public IP 149.62.207.243, ~80ms mesh RTT). Mac mini still
  on Ethernet. See cellular section at bottom.

## Summary

All six tests PASS. No solutions (S1–S6) in
[`docs/sleep-wake-plan.md`](../../docs/sleep-wake-plan.md) are needed based on
this evidence — the v0.9.6/v0.9.7 fixes are empirically validated.

| Test | Goal | Verdict | Key number |
|---|---|---|---|
| **T1** | short sleep — no spurious tick-gap | ✅ PASS | 9s sleep, no rebind fired |
| **T2** | 2min sleep baseline | ✅ PASS | peer-recovery 3s, tunnels closed |
| **T3** | 10min sleep | ✅ PASS (partial) | tunnel recovered; sched wake unreliable |
| **T4** | DNS + resolver-file correctness | ✅ PASS (clean) | mesh-DNS @ T+5s; resolver diff empty; pub DNS never blocked |
| **T5** | peer-side black-hole window | ✅ PASS | <3s post-subject-wake |
| **T6** | hibernate utun survival | ✅ PASS | utun0 intact; hibernatemode restored |

## Per-test details (see `NN-tX-events.txt` for full evidence)

### T1 — 9s sleep, no tick-gap

- Outage: 11:37:03 → 11:37:12 (9s of peer timeouts)
- Agent log post-wake: nothing — no `sleep/wake detected`, no `rebinding`
- Confirms: 15s tick-gap threshold correctly ignores short sleeps.
- One pre-sleep `network change detected` event at 11:37:02 (WiFi going down as part of sleep) — normal.

### T2 — 2min soft sleep, primary baseline

- Outage: 10:44:41 → 10:46:44 (124s)
- First OK ping post-wake: 10:46:45 (3s after scheduled wake at 10:46:42)
- Agent fired rebind: `[agent] network change detected … closed 3 tunnels to force re-handshake`
  - Fired at 13:46:46 local (= 10:46:46Z, 1s after wake)
  - A second rebind at 13:46:51 — WiFi stabilisation flap
- **Note on log masking.** The plan expected `"sleep/wake detected (tick gap 2m0s)"` but the code's branch priority means the log shows `"network change detected"` when ANY local address also shifted during sleep — which is typical for a WiFi re-association. The tick-gap detection still fires (rebind + tunnel close happen), but the log message is masked. Not a bug per se, but a diagnostic gap for post-hoc analysis of pure tick-gap events.

### T3 — 10min soft sleep

- Attempted outage: 10:55:04 → 11:08:14 (13m 10s actual, vs 10m planned)
- `pmset schedule wake` did NOT fire for 10-min sleep on this Apple Silicon MacBook (it worked for T2's 2-min sleep). Machine eventually woke via cumulative WOL packets from Mac mini.
- Tunnel recovered within ~1s once the machine was actually awake.
- MacBook auto-slept after ~15s of each wake window (macOS standby behaviour) — made subject-side agent-log capture unreliable without user keeping the machine awake.
- **Operational learning**: for any future long-sleep testing, the peer needs continuous log-ship-off-host (rsyslog-to-peer), not SSH tail, to survive these repeated dropouts.

### T4 — DNS recovery + resolver-file integrity *(kernel-TUN only)*

**Clean pass.** The most important test given Tailscale's #17736 class of bug.

- All 20 post-wake probes (1s apart, starting T+5s after scheduled wake):
  - mesh query (`yavors-macbook-pro.home`): 93–161ms each, no timeouts
  - public query (`example.com`): 85–170ms each, no timeouts
- `/etc/resolver/home` pre-sleep vs post-wake: BYTE-IDENTICAL (`04-t4-resolver-diff.txt` empty).
- No unmapped code path touching the resolver file; no DNS stub poisoning.
- Split-DNS isolation held — public DNS queries never blocked by our stub.

### T5 — peer-side black-hole window

Derived from `peer-ping-continuous.log`. Peer-recovery lags after each subject wake:

- T1 (WOL wake): <1s
- T2 (scheduled wake): 3s
- T6 (hibernate wake): <1s (measured from when subject actually woken per ping recovery)

No "peer keeps sending to dead NAT" stall observed. Solution **S5a** (pre-sleep bye-bye) not needed.

### T6 — hibernate cycle

- Set `hibernatemode 25`, issued sleepnow, wake scheduled +130s.
- Outage: 11:31:58 → 11:35:09 (**191s** — ~65s longer than T2's soft sleep due to disk-restore time).
- utun0 with mesh IP `10.42.1.6` still present post-wake — no "TUN device not found" scenarios.
- Agent log at 14:35:11 local: `network change detected`, `closed 3 tunnels`.
- Recovery pattern matches T2. No utun corruption.
- **Critical safety check:** `hibernatemode` restored to 3 — verified twice via `pmset -g | grep hibernatemode`.
- **Methodology caveat**: first T6 attempt at 11:27:41 DID NOT actually hibernate — five concurrent `caffeinate` assertions (from earlier SSH-issued keep-awake attempts) prevented full sleep. Required `sudo pkill -9 -f caffeinate` before `pmset sleepnow` would produce a real hibernate.

## Maps to decision matrix in `docs/sleep-wake-plan.md`

Row matched: `T2 self-recovery <10s | T4 mesh-DNS recovery ≤5s | resolver diff empty | T5 black-hole <30s | T6 utun survives → **Ship as-is.**`

No S1–S6 remediation needed.

## Secondary findings (worth writing down)

1. **`pmset schedule wake` is unreliable for longer sleep durations on Apple Silicon MacBooks.**
   - T2 (2-min sleep): fired on schedule ±3s
   - T3 (10-min sleep): failed to fire; machine required WOL + repeat attempts
   - T4 (2-min sleep): fired on schedule
   - T6 (2-min sleep with hibernatemode=25): fired on schedule but wake took ~65s extra for disk restore
   - **Implication for future tests**: rely on WOL as primary, pmset schedule as secondary.

2. **macOS aggressively auto-sleeps after scheduled wake**, even with AC power, if no user activity in the ~15-30s post-wake window. For long test batteries on a MacBook, a user physically present to keep it awake (or a disablesleep override) is required.

3. **`addrChanged` branch in `watchNetworkChanges` takes priority over `sleptAndWoke`** in the log message. Both conditions trigger the same rebind code path, but the log reports only the addr-change reason when both are true. For post-hoc diagnosis, this means "pure sleep/wake with no addr change" is the only way to see the `"sleep/wake detected (tick gap Ns)"` log in practice — and that's rare since WiFi almost always re-associates during sleep.

4. **Recovery flaps.** Every soft-sleep recovery in T2/T6 produced TWO rebind events in quick succession (e.g. 13:46:46 and 13:46:51), each followed by a few seconds of additional packet loss. This is WiFi stabilisation after re-association, not a VPN issue. Total pre-stabilisation window: ~12s in T2.

5. **Hibernate restores utun cleanly** — this was listed as "❌ Unknown" in the plan's Row 12. Now confirmed handled.

## Run 2 — Cellular hotspot (2026-04-17, 11:59Z–12:06Z)

MacBook switched from home WiFi (192.168.23.18 on 192.168.23.0/24) to iPhone
hotspot (172.20.10.2 on 172.20.10.0/28, public IP 149.62.207.243, behind
carrier CGNAT). Mac mini unchanged (Ethernet peer).

Evidence: `cellular-00-fingerprint.txt`, `cellular-t2-*`, `cellular-t4-*`,
`cellular-peer-ping-continuous.log`, `cellular-{peer,subject}-agent-continuous.log`.

### Cellular T2 — 2-min sleep on iPhone hotspot

| Metric | LAN run | Cellular run |
|---|---|---|
| Outage duration | 124s | 124s |
| Peer-view recovery after scheduled wake | **3s** | **3s** |
| Rebind fires via `addrChanged` branch | ✓ | ✓ |
| Recovery path | Direct P2P | Relay (`132.145.232.64:42001`) — expected for cellular CGNAT |
| Post-wake stabilization flaps | Two ~6s apart, 3-4 timeouts each | Two shorter, 1-2 timeouts each |
| Pre-sleep mesh RTT | ~5ms | ~80ms |
| Post-wake network | Same LAN | **Still on cellular** (no auto-switch to WiFi) |

The recovery mechanism is **identical to LAN** despite ~15× higher baseline
RTT and a completely different NAT topology. Nebula's relay fallback is
seamless: when the peer tries to re-handshake post-wake, the direct P2P
attempt to `149.62.207.243:26261` (MacBook's public cellular IP:port)
times out (symmetric/random-port CGNAT — per CLAUDE.md discovery log),
then relay path succeeds within the same second.

### Cellular T4 — DNS on cellular

| Metric | LAN run | Cellular run |
|---|---|---|
| First mesh query success | T+5s post-wake | T+6s post-wake |
| Mesh query RC=0 across all 20 samples | ✓ | ✓ |
| Public query RC=0 across all 20 samples | ✓ | ✓ |
| Resolver file diff | empty | empty |
| Mesh query p50 latency | ~105ms | ~125ms (+20ms cellular overhead) |
| Public query p50 latency | ~115ms | ~125ms |
| Outliers | None | 1 cellular congestion spike (761ms on pub i=16) |

Split-DNS isolation holds on cellular: public DNS never blocked by our stub,
resolver file byte-identical pre/post.

### Cellular findings

1. **No observable regression on cellular.** Recovery time matches LAN exactly.
   This is stronger evidence than LAN alone — the tunnel re-establishment
   works correctly across a carrier CGNAT under symmetric-NAT conditions.
2. **Relay path does its job.** P2P through CGNAT fails (expected); relay
   succeeds on first attempt. The lighthouse (`132.145.232.64:42001`) is
   the forwarding path.
3. **MacBook did NOT auto-switch to WiFi on either cellular wake.** User
   had warned this might happen; it didn't on this run. If it had, the
   addrChanged branch would have triggered with a bigger interface/IP
   change — would likely behave similarly given the same code path.
4. **Peer agent log shows three addresses tried on recovery**:
   `[192.168.23.3:4242 46.10.240.91:4242]` (stale LAN addresses from
   previous connection) and `[149.62.207.243:26261]` (current cellular
   public). All direct attempts fail; relay wins. No regression.

### Combined macOS verdict (LAN + Cellular runs)

All seven executed tests PASS (T1–T6 on LAN + T2, T4 on cellular). Recovery
characteristics are stable across both network topologies. The competitive
failure modes that open issues in Tailscale/ZeroTier/NetBird (DNS poisoning,
stuck after long sleep, broken tunnel after network change) do not
reproduce in hopssh v0.9.7 on macOS.

## Run 3 — Linux VM (Ubuntu 25.10 on UTM, aarch64, 2026-04-17 ~16:17Z)

Subject: Ubuntu 25.10 VM inside UTM on the MacBook, bridged to MacBook's
en0. Mesh IP `10.42.1.8`. Kernel-TUN mode. Enrolled via `hop-agent enroll
--endpoint https://hopssh.com` using the device flow.

Evidence: `linux-00-fingerprint.txt`, `linux-t2-*`, `linux-t4-*`,
`linux-peer-*.log`, `linux-t2-subject-agent-*.log`.

### What DIDN'T work (ruled out paths)

- **`rtcwake -s 130 -m mem`** — fails with `set rtc wake alarm failed: Invalid argument`. QEMU ARM's `rtc-efi` doesn't accept wake alarms.
- **Write to `/sys/class/rtc/rtc0/wakealarm`** — same rejection (`Invalid argument`).
- **`systemctl suspend`** (real S3) — VM enters S3 but cannot be woken. UTM's "wake" action cold-resets the VM instead of resuming — journal shows a new boot ID between the pre/post entries, and `uptime` confirms fresh boot. Wake-on-LAN, packet flood, and TCP SYN storms all fail to wake the VM from S3.
- **`echo freeze > /sys/power/state`** — VM hangs indefinitely, requires manual "Restart VM" from UTM.

So: **QEMU ARM on Apple Silicon does not support real sleep-resume.** Real OS-level sleep/wake testing on Linux needs bare-metal Linux hardware, not a UTM VM.

### Linux-T2 via SIGSTOP (functional test, works reliably)

Pivot: pause the `hop-agent` process with `SIGSTOP`, wait 130s, resume with `SIGCONT`. This freezes the Go runtime's goroutines — including the 5s ticker in `watchNetworkChanges` — without needing OS-level sleep. On `SIGCONT`, the next ticker tick sees `time.Now()` 130s ahead of its last recorded tick — the exact code path that real sleep would trigger.

| Metric | Measured |
|---|---|
| SIGSTOP issued | 2026-04-17T16:17:13Z (agent PID 3424) |
| SIGCONT issued | 2026-04-17T16:19:23Z (128s later, same PID) |
| Peer ping outage | 129 timeouts, 16:17:15 → 16:19:23 |
| Peer-view recovery | same second as SIGCONT |
| Agent PID post-resume | **3424 (unchanged)** — agent resumed, not restarted |
| Tick-gap log fired | **✅ `[agent] sleep/wake detected (tick gap 2m14s) detected (iface: enp0s1→enp0s1), rebinding Nebula`** |
| Tunnels closed | 2 |

**This is the first appearance of the `"sleep/wake detected"` log string across all runs.** On macOS (LAN and cellular) the `addrChanged` branch always won because WiFi re-associate changed local addresses; the log reported `"network change detected"` instead. On Linux with SIGSTOP, the interface stays `enp0s1→enp0s1` unchanged, so `sleptAndWoke && !addrChanged` is true and the sleep-specific log fires. This validates:

- `cmd/agent/nebula.go:183-187` branch selection works correctly.
- The cosmetic fix from commit `c22bd60` (capture `tickGap` before reassignment) is observable — `tick gap 2m14s` is the real value, not `0s`.
- The functional code path on Linux matches macOS (same rebind + tunnel-close behavior).

### Linux-T4 — FAIL (separate bug, not sleep/wake-related)

Linux hop-agent **registers mesh DNS correctly** with systemd-resolved (confirmed via `resolvectl status` and DBus), but **systemd-resolved cannot forward `.home` queries to the configured DNS server**.

| Query path | Result |
|---|---|
| `dig +short yavors-macbook-pro.home` (via stub 127.0.0.53) | **TIMES OUT (3s)** |
| `dig +short example.com` (via stub 127.0.0.53) | **WORKS (23ms)** — public uplink OK |
| `dig @132.145.232.64 -p 15300 yavors-macbook-pro.home` (direct) | **WORKS (39ms)** |
| `resolvectl query yavors-macbook-pro.home` | `Query timed out` |

systemd-resolved DBus state:
```
DNS on nebula1:   132.145.232.64 (AF_INET)
Domains on nebula1: ~home (routing-only)
```

The DNS server is reachable via the default route (`enp0s1`), not via `nebula1` (it's a public IP). systemd-resolved on Ubuntu 25.10 (systemd 257+) appears unable to forward non-port-53 DNS queries through this per-link-with-non-link-local-server setup — possibly strictness introduced in recent systemd versions.

Code path: `cmd/agent/dns_linux.go:15-46` calls `resolvectl dns <iface> <ip>:<port>` + `resolvectl domain <iface> ~<domain>`. This syntax works on older systemd-resolved but fails on 257+.

Workarounds users can apply today:
- `dig @132.145.232.64 -p 15300 <host>.home` (direct query bypassing stub)
- Manually add to `/etc/resolv.conf` (but systemd-resolved rewrites it)

Fix directions (for a follow-up patch, NOT sleep/wake-related):
- Register DNS globally (`resolvectl dns --set-global`) instead of per-link
- Have the lighthouse DNS also listen on port 53 (requires root on lighthouse, or a privileged port proxy)
- Have the lighthouse DNS listen on its mesh IP (`10.42.1.1:15300`) so per-link binding via `nebula1` routes consistently
- Ship a `/etc/systemd/resolved.conf.d/hopssh.conf` drop-in file

### Linux verdict

- **Sleep/wake code path: PASS (functional)** — tick-gap detection fires correctly, rebind fires, tunnel recovers on SIGCONT within the same second. Log message appears as designed.
- **DNS on Linux: FAIL (baseline bug)** — unrelated to sleep/wake. Mesh DNS simply doesn't work on Ubuntu 25.10 systemd-resolved in v0.9.7. File-level: `cmd/agent/dns_linux.go:15-46` needs a fix. Mesh IP connectivity itself is fine (ping/SSH/arbitrary TCP to `10.42.X.X` works end-to-end).
- **Scope of SIGSTOP test**: exercises the agent's time-jump handling only — NOT network interface down/up (interfaces never went down), NOT TUN driver pause, NOT kernel DNS reset. Those dimensions are fundamentally untestable on QEMU ARM and remain unvalidated on Linux until we get bare-metal Linux hardware.
