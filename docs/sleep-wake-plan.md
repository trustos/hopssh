# Sleep/Wake Resilience — Test Plan & Gap Analysis

*2026-04-17. Follows the sleep/wake fix in v0.9.6 (commit 606384b).*

---

## What we shipped (v0.9.6)

Two fixes for "Screen Sharing broken after overnight sleep":

1. **Tick-gap detection** (`cmd/agent/nebula.go:166-197`): if the 5s polling ticker
   fires and >15s have passed, force `RebindUDPServer()` + `CloseAllTunnels(true)`.
   Catches sleep/wake on the same WiFi where the address fingerprint doesn't change.

2. **Cert-expired cold-start** (`cmd/agent/renew.go:517-560`): when
   `reloadNebula()` finds `currentNebula == nil` (Nebula failed at boot due to
   expired cert), it starts Nebula fresh with `startMesh()` instead of returning
   "no embedded Nebula instance to reload."

Observed once on macOS (Mac mini ↔ MacBook Pro, same LAN) during the fix session:

- Bug 2: 20s lid-close → rebind fired on wake → tunnel recovered in seconds.
- Bug 1: corrupted cert → OS stack → 5 min renewal → `"Nebula started after cert renewal"` → pings flowing.

**No structured evidence was captured.** The test plan below is designed to
produce reproducible measurements in `spike/sleep-wake-evidence/`. Until that
exists, treat the v0.9.6 fix as "observed to work once," not "validated."

> **Bundled cosmetic fix.** The first version of the tick-gap path logged
> `"tick gap 0s"` for every event because `lastTick` was reassigned before the
> format string read it. Fixed in the same commit as this doc revision so the
> log now reports the real gap — essential for post-hoc diagnosis during T2/T3.

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
| **WireGuard** | None. Recovery requires `PersistentKeepalive` — **default 0 (off)**; 25s if configured. Idle peers with no keepalive never auto-recover until the next outgoing packet triggers a handshake. | systemd 253 deletes routing rules on suspend (external breakage) |
| **ZeroTier** | None documented — waits for protocol-level peer re-discovery via roots. | 1-10+ minute recovery (#2026, #2545). Sometimes requires reboot. |
| **NetBird** | Added macOS sleep detection (v0.60.4, further iterations through v0.66.3, March 2026), but policy bugs (auto-reconnects after intentional disconnect). | #2454, #632, #3880 — actively iterating. |

**Key insight (calibrated):** Tailscale's `TimeJumped` is equivalent to our
tick-gap and they've had it for years. The fact that #17736 / #10688 / #1554
are still open after years of active engineering means sleep/wake is a
genuinely hard problem, not a class of edge cases Tailscale shrugs at. Our
v0.9.6 fix is a **floor** (parity with their core detection), not a ceiling.

---

## Failure mode matrix

| # | Failure mode | hopssh status | Priority |
|---|---|---|---|
| 1 | Stale UDP sockets (NAT expired) | ✅ **Handled** — tick-gap → rebind | — |
| 2 | Cert expired during sleep | ✅ **Handled** — cold-start in `reloadNebula()` | — |
| 3 | Address fingerprint change on wake | ✅ **Handled** — existing `watchNetworkChanges` | — |
| 4 | **DNS stub poisoning** — split-DNS resolver unreachable during recovery window, queries hang | ⚠️ **UNKNOWN — must test (kernel-TUN mode only)** | **HIGH** |
| 5 | Power Nap / dark wake (process runs, WiFi may be unavailable) | ❌ Not handled (tick-gap won't fire — process isn't suspended) | LOW (macOS edge case) |
| 6 | Very short sleep (<15s) | ⚠️ Tick-gap won't fire, but NAT survives. Natural recovery via keepalive. | LOW |
| 7 | systemd routing rule deletion (Linux) | ✅ Not vulnerable (Nebula uses TUN, not policy rules) | — |
| 8 | Wake to different WiFi (different SSID/subnet) | ✅ **Handled** — fingerprint changes → rebind | — |
| 9 | Dock/undock (WiFi ↔ Ethernet) | ✅ **Handled** — fingerprint includes all interfaces | — |
| 10 | Peer-side black-hole window (peer sends to old NAT until its own tunnel test fires) | ⚠️ Partial — ~15s test cadence, but unmeasured. T5 quantifies this. | MEDIUM |
| 11 | Recovery ordering (rebind before WiFi reassociates) | ⚠️ Partially — we rebind immediately; if WiFi isn't up, bind may fail. But `CloseAllTunnels(true)` forces rehandshake which retries. | MEDIUM |
| 12 | TUN device corruption after hibernation | ❌ Unknown — utun created at startup, not recreated after wake. Covered by new T6. | LOW |
| 13 | Battery vs speed tradeoff | ❌ Not considered — rebind on every tick-gap unconditionally. Matters for mobile. | LOW (no mobile clients) |

---

## Test plan

One sitting, two Macs, ~45 minutes end-to-end. All output lands in
`spike/sleep-wake-evidence/` following the `spike/nebula-baseline-evidence/`
convention (numbered plain-text logs + a README summarising setup and results).

### Topology (fixed for every test)

| Role | Hardware | Network | Overlay IP | Behaviour |
|---|---|---|---|---|
| **Subject** | MacBook Pro | WiFi | `10.42.1.6` | Sleeps via `pmset sleepnow`, then wakes |
| **Peer / observer** | Mac mini | Ethernet | `10.42.1.7` | Never sleeps; runs continuous ping + log tail |

Both agents must be v0.9.6+ with the tick-gap-log fix (first rebind event
should log `"sleep/wake detected (tick gap Ns)"` with `N > 0`).

### Per-test contract

Every test block below has the same shape so evidence is comparable across runs:

1. **Goal** — one line, pointing at the code path being exercised.
2. **Failure-mode row** — which entry in the matrix above this test covers.
3. **Prerequisites** — especially TUN mode, since T4 is kernel-only.
4. **Procedure** — copy-pasteable bash, `pmset sleepnow` (scripted — lid-close
   has too much timing variance; we learned this on the QUIC cellular tests).
5. **Expected log lines** — quoted verbatim from code so you can grep for them.
6. **Pass criteria** — specific numbers, not "feels fast."
7. **Fail → which solution** (S1–S6 below) the failure justifies building.
8. **Evidence files** — exact paths produced.

### Prerequisites — one-time setup

**Evidence directory:**

```bash
mkdir -p spike/sleep-wake-evidence && cd "$_"
```

**Fingerprint (`00-fingerprint.txt`)** — captured before any test runs so every
finding below can reference the state it started from. Run on BOTH nodes,
concatenate outputs, label each block with the hostname:

```bash
{
  echo "=== $(hostname) $(date -u '+%Y-%m-%dT%H:%M:%SZ') ==="
  hop-agent version
  hop-agent status || true
  hop-agent info   || true
  echo "--- tun-mode ---"
  cat ~/.hop-agent/tun-mode 2>/dev/null || echo "(not set — userspace)"
  echo "--- /etc/resolver ---"
  ls -la /etc/resolver/ 2>/dev/null || echo "(none)"
  for f in /etc/resolver/*; do [ -f "$f" ] && { echo "### $f"; cat "$f"; }; done
  echo "--- scutil --dns (supplemental) ---"
  scutil --dns 2>/dev/null | grep -B1 -A5 "SupplementalMatchDomain" || echo "(none)"
  echo "--- hibernatemode ---"
  pmset -g | grep hibernatemode
} | tee -a 00-fingerprint.txt
```

**Gating checks** (abort the run if any fail — else the later evidence is
uninterpretable):

- `hop-agent status` on subject reports cert expiry **>24h** — otherwise a
  cert-renewal during a long test confounds the sleep-wake signal.
- `ping -c 3 10.42.1.7` from subject succeeds — baseline tunnel is up.
- `ping -c 3 10.42.1.6` from peer succeeds — return path is up.
- Subject's `hibernatemode` value recorded in the fingerprint — **required** for
  T6 cleanup (we have to restore it).

**Continuous captures (start before T1, leave running through T6):**

Peer (Mac mini, one tmux pane per capture):

```bash
# Continuous ping with millisecond UTC timestamps
ping -i 1 10.42.1.6 \
  | while IFS= read -r line; do
      printf '%s %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%S.%3NZ')" "$line"
    done \
  | tee spike/sleep-wake-evidence/peer-ping-continuous.log

# Filtered agent log (see cmd/agent/nebula.go:166-197 + renew.go:517-560 for the strings we want)
sudo tail -f /var/log/hop-agent.log \
  | grep --line-buffered -E "sleep/wake|network change|rebinding|closed .* tunnels|Handshake|Nebula started" \
  | tee spike/sleep-wake-evidence/peer-agent-continuous.log
```

Subject (MacBook, one tmux pane):

```bash
sudo tail -f /var/log/hop-agent.log \
  | grep --line-buffered -E "sleep/wake|network change|rebinding|closed .* tunnels|Handshake|Nebula started" \
  | tee spike/sleep-wake-evidence/subject-agent-continuous.log
```

**Timeline convention** — for each test, append to a per-test `NN-tX-events.txt`:

```
T-0    pre-sleep:   <date -u '+%Y-%m-%dT%H:%M:%S.%3NZ'>
T-1    sleep issued (pmset sleepnow)
T-2    wake observed (first shell prompt redraw after wake)
T-3    first successful subject-side ping
T-4    first rebind log line after T-2 (copy tick-gap value verbatim)
T-5    first successful peer-side ping after T-1
```

Recovery metrics derived: self-recovery = `T-3 − T-2`; peer-recovery = `T-5 − T-1`;
black-hole window (T5) = `T-5 − T-2`.

---

### T1 — Short sleep (10s), same WiFi

**Goal.** Verify NAT mapping survives a sub-threshold sleep; tick-gap does NOT fire.
**Failure mode.** Row 6 — very short sleep (<15s).
**Prerequisites.** Both TUN modes acceptable.

```bash
# On subject, new pane
NN=01; T=t1
mkdir -p spike/sleep-wake-evidence
{
  printf 'T-0 pre-sleep: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%S.%3NZ')"
  ping -c 2 10.42.1.7 >>spike/sleep-wake-evidence/${NN}-${T}-subject-ping.log 2>&1
  printf 'T-1 sleep-issued: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%S.%3NZ')"
  sudo pmset sleepnow          # 10s is short enough that we wake by key before pmset completes in practice;
                               # fallback: `caffeinate -u -t 1` scheduled 10s out, or manual key-press
  # -- wait 10s, then wake the machine with a key-press --
  printf 'T-2 wake-observed: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%S.%3NZ')"
  for i in 1 2 3 4 5; do
    ping -c 1 -W 2000 10.42.1.7 | tail -1
  done
  printf 'T-3 first-success: (see log lines above)\n'
} | tee spike/sleep-wake-evidence/${NN}-${T}-events.txt
```

**Expected log lines** — subject agent log should contain NONE of these for
this test (10s < 15s tick-gap threshold):

- `[agent] sleep/wake detected (tick gap ...) detected (iface: ... rebinding Nebula`
- `[agent] closed N tunnels to force re-handshake on new network`

**Pass.** Pings resume on first attempt after wake. Subject agent log shows no
`sleep/wake detected` entry for this window. Peer ping log shows ≤3 consecutive
`Request timeout` entries around T-1.
**Fail → action.** If tick-gap does fire at 10s, threshold is too tight — file
an issue to revisit the `>15*time.Second` check in `cmd/agent/nebula.go:176`.
**Evidence.** `01-t1-events.txt`, `01-t1-subject-ping.log`.

---

### T2 — Medium sleep (2 min), same WiFi  *(primary baseline)*

**Goal.** Verify tick-gap fires, rebind happens, tunnel recovers within 10s of wake.
**Failure modes covered.** Rows 1, 3, 8, 11.
**Prerequisites.** Both TUN modes acceptable. Same-WiFi verified.

```bash
NN=02; T=t2
{
  printf 'T-0 pre-sleep: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%S.%3NZ')"
  printf 'T-1 sleep-issued: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%S.%3NZ')"
  sudo pmset sleepnow
  # -- wait 120s, then wake --
  printf 'T-2 wake-observed: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%S.%3NZ')"
  for i in $(seq 1 10); do
    printf '%s ping #%d: %s\n' "$(date -u '+%H:%M:%S.%3N')" "$i" \
      "$(ping -c 1 -W 2000 10.42.1.7 2>&1 | tail -1)"
    sleep 1
  done
} | tee spike/sleep-wake-evidence/${NN}-${T}-events.txt

# After T-3 visible, snapshot the relevant agent log window
sudo grep -E "sleep/wake|rebinding|closed .* tunnels" /var/log/hop-agent.log \
  | tail -20 \
  > spike/sleep-wake-evidence/02-t2-subject-agent-window.log
```

**Expected log lines** (tick-gap ≈ 2m depending on scheduler jitter):

```
[agent] sleep/wake detected (tick gap 2m0s) detected (iface: en0→en0), rebinding Nebula
[agent] closed N tunnels to force re-handshake on new network
```

**Pass.**
- Exactly one `sleep/wake detected` line appears, with a non-zero tick-gap value.
- `closed N tunnels` line appears with `N ≥ 1`.
- Self-recovery `T-3 − T-2 < 10s`.
- No repeated handshake failures in the 30s after wake.

**Fail → action.**
- No `sleep/wake detected` line → detection broken; re-verify tick-gap fix
  (`cmd/agent/nebula.go:175-187`).
- `closed 0 tunnels` → either tunnels had already died (check peer log) or the
  kill path isn't reaching them; investigate before promoting any solution.
- Self-recovery >30s → promote **S1** (OS-level wake notification) and **S4**
  (faster rebind retry) to roadmap; attach this evidence.

**Evidence.** `02-t2-events.txt`, `02-t2-subject-agent-window.log`,
and ping gap visible in `peer-ping-continuous.log` around T-1…T-5.

---

### T3 — Long sleep (10 min), same WiFi

**Goal.** Confirm 10× duration doesn't regress vs T2. Cert still valid (>24h from fingerprint).
**Failure modes covered.** Rows 1, 3 at stress duration.

Identical procedure to T2 with `sleep 600`. Write to `03-t3-*.txt/log`.

**Pass.** Same as T2 (tick-gap ≈ 10m0s, self-recovery <10s, no anomalies).
**Fail → action.**
- Handshake retry storm in log → lighthouse may be unreachable post-wake; check
  peer `peer-agent-continuous.log` for lighthouse-side events.
- Recovery noticeably slower than T2 → promote **S1** + **S4**.

**Evidence.** `03-t3-events.txt`, `03-t3-subject-agent-window.log`.

---

### T4 — Mesh-DNS recovery + stale-resolver check  *(kernel-TUN only)*

**Goal (reframed from v1).** hopssh's `/etc/resolver/<domain>` split-DNS
(`cmd/agent/dns_darwin.go:11-74`) routes only `*.<domain>` to our resolver,
so public DNS cannot be poisoned by our stub. Two concrete questions instead:

1. **Recovery latency** — how long after wake until `dig <host>.<domain>` succeeds?
2. **Resolver-file correctness** — is `/etc/resolver/<domain>` byte-identical
   pre-sleep vs post-wake? Code only calls `configureDNS()` at startup and on
   cert-renewal cold-start (`renew.go:539-541`); not from `reloadNebula()` warm
   path or `watchNetworkChanges` rebind. If the file DID change, an unexpected
   code path is touching it.

**Failure mode covered.** Row 4.
**Prerequisites.**
- Subject's `~/.hop-agent/tun-mode` == `kernel` (from fingerprint). Skip this
  test and mark Row 4 N/A if userspace — there's no system-level DNS config.
- Discover the mesh domain from the fingerprint: `DOMAIN=$(ls /etc/resolver/ | head -1)`.

```bash
NN=04; T=t4
DOMAIN=$(ls /etc/resolver/ 2>/dev/null | head -1)
[ -n "$DOMAIN" ] || { echo "no /etc/resolver — skip T4"; exit 1; }

# Capture pre-sleep DNS state
cp /etc/resolver/"$DOMAIN" spike/sleep-wake-evidence/${NN}-${T}-resolver-pre.txt
scutil --dns > spike/sleep-wake-evidence/${NN}-${T}-scutil-pre.txt

{
  printf 'T-0 pre-sleep: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%S.%3NZ')"
  printf 'T-1 sleep-issued: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%S.%3NZ')"
  sudo pmset sleepnow
  # -- wait 120s, then wake --
  printf 'T-2 wake-observed: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%S.%3NZ')"

  # Race the recovery window: probe once per second for 15s
  for i in $(seq 1 15); do
    MESH_RC=$(dig +short +time=2 +tries=1 "$(hostname)"."$DOMAIN" >/dev/null 2>&1; echo $?)
    PUB_RC=$(dig +short +time=2 +tries=1 example.com           >/dev/null 2>&1; echo $?)
    printf '%s t=%02ds mesh_rc=%s pub_rc=%s\n' \
      "$(date -u '+%H:%M:%S.%3N')" "$i" "$MESH_RC" "$PUB_RC"
    sleep 1
  done
} | tee spike/sleep-wake-evidence/${NN}-${T}-events.txt

# Capture post-wake DNS state
cp /etc/resolver/"$DOMAIN" spike/sleep-wake-evidence/${NN}-${T}-resolver-post.txt
scutil --dns > spike/sleep-wake-evidence/${NN}-${T}-scutil-post.txt

# Diff — should be empty
diff spike/sleep-wake-evidence/${NN}-${T}-resolver-pre.txt \
     spike/sleep-wake-evidence/${NN}-${T}-resolver-post.txt \
     > spike/sleep-wake-evidence/${NN}-${T}-resolver-diff.txt || true
```

**Pass.**
- `pub_rc=0` on every sample (public DNS never blocked).
- `mesh_rc=0` first observed within 5 samples (≤5s) of T-2.
- `resolver-diff.txt` empty — file unchanged.

**Fail → action.**
- `pub_rc` non-zero on any sample → split-DNS isolation has broken; file an
  issue, this is a genuine regression of the split-DNS assumption.
- `mesh_rc=0` not seen within 15s → **S3** (DNS stub suspend/resume) becomes relevant.
- `resolver-diff.txt` non-empty → unmapped code path is rewriting the resolver
  file. Grep `configureDNS` / `os.WriteFile` in `cmd/agent/dns*.go` to find it;
  may indicate a dormant bug.

**Evidence.** `04-t4-events.txt`, `04-t4-resolver-pre.txt`,
`04-t4-resolver-post.txt`, `04-t4-resolver-diff.txt`,
`04-t4-scutil-pre.txt`, `04-t4-scutil-post.txt`.

**T4b — stale-target simulation (optional, defer if infeasible).**
Change the lighthouse DNS endpoint on a non-prod control plane while the
subject is asleep, then wake. If `mesh_rc` stays failing until the agent is
restarted, we've proven the static-resolver-file bug and **S3** (or a new
"rewrite resolver on rebind" solution) is needed. Skip if no second control
plane is available this session — mark Row 4 with a T4b-deferred note.

---

### T5 — Peer-side black-hole window (post-hoc analysis)

**Goal.** Quantify how long the peer keeps sending to the subject's dead NAT
binding before Nebula's connection-manager test (~15s cadence) tears down the
stale tunnel. This is a measurement, not a new sleep — derived from the
`peer-ping-continuous.log` already captured across T2 and T3.

**Failure mode covered.** Row 10.

```bash
# On peer, after T3 completes. Pull the T2 and T3 windows out of the peer log.
awk '/Request timeout/ || /bytes from/' \
  spike/sleep-wake-evidence/peer-ping-continuous.log \
  > spike/sleep-wake-evidence/05-t5-peer-timeline.log

# Manual read: for each of T2/T3, find the first "Request timeout" after T-1
# and the first "bytes from 10.42.1.6" after T-2. The gap = black-hole window.
```

**Pass.** Black-hole window <30s for both T2 and T3 (≈ Nebula tunnel-test cadence).
**Fail → action.** >30s → promote **S5a** (pre-sleep bye-bye packet) to roadmap.
**Evidence.** `05-t5-peer-timeline.log`, measurements written into `RESULTS.md`.

---

### T6 — Hibernate cycle  *(utun survival after suspend-to-disk)*

**Goal.** Cover Row 12 — does utun survive a real hibernate (`hibernatemode 25`)
or does the FD get invalidated?
**Failure mode covered.** Row 12.
**Prerequisites.** **Record original `hibernatemode` from `00-fingerprint.txt`**
and restore it at the end. macOS default is usually `3` or `25` depending on
hardware; don't leave the user in the wrong mode.

```bash
NN=06; T=t6
ORIG_HIB=$(pmset -g | awk '/hibernatemode/ {print $2}')
echo "original hibernatemode: $ORIG_HIB" \
  > spike/sleep-wake-evidence/${NN}-${T}-events.txt

sudo pmset -a hibernatemode 25
{
  printf 'T-0 pre-hibernate: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%S.%3NZ')"
  printf 'T-1 sleep-issued: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%S.%3NZ')"
  sudo pmset sleepnow
  # -- wait 120s, wake via power button (hibernate restore is slower than sleep) --
  printf 'T-2 wake-observed: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%S.%3NZ')"
  ifconfig | awk '/^utun/ {print; getline; print}' | head -20
  ping -c 10 -i 1 10.42.1.7 | tee -a /dev/stderr
} | tee -a spike/sleep-wake-evidence/${NN}-${T}-events.txt

sudo grep -E "utun|tun device|Nebula started" /var/log/hop-agent.log \
  | tail -20 > spike/sleep-wake-evidence/${NN}-${T}-agent-window.log

# CLEANUP — MUST run even if the test aborted
sudo pmset -a hibernatemode "$ORIG_HIB"
pmset -g | grep hibernatemode >> spike/sleep-wake-evidence/${NN}-${T}-events.txt
```

**Pass.** utun interface still present post-wake; pings recover (same criteria as T2).
**Fail → action.**
- utun present but rebind doesn't recover tunnel → same remediation path as T2
  failures (S1/S4).
- utun absent / `tun device not found` in log → promote **S6** (TUN-health probe
  + cold restart via existing `reloadNebula()` path) to roadmap.

**Cleanup verification (MANDATORY).** `pmset -g | grep hibernatemode` must
match the pre-test value from `00-fingerprint.txt`. If this doesn't match,
fix it before closing the session — don't leave the subject in the wrong mode.

**Evidence.** `06-t6-events.txt`, `06-t6-agent-window.log`.

---

## Decision matrix (fill after the run, write to `spike/sleep-wake-evidence/RESULTS.md`)

| T2 self-recovery | T4 mesh-DNS recovery | T4 resolver diff | T5 peer black-hole | T6 utun | Action |
|---|---|---|---|---|---|
| <10s | ≤5s | empty | <30s | survives | **Ship as-is.** Update `docs/features.md` + competitive analysis with measured numbers. No solutions needed. |
| <10s | >5s to 15s | empty | <30s | survives | **Low priority.** Note in roadmap; mesh DNS recovery is acceptable UX. |
| <10s | >15s (times out) | empty | <30s | survives | **Medium.** Promote **S3** (DNS stub suspend/resume) — measured stub is too slow on wake. |
| <10s | any | **non-empty** | any | any | **Bug.** Unmapped code path rewriting resolver file. Investigate before any other solution. |
| 10–30s | any | any | any | any | **Medium.** Promote **S4** (faster rebind retry) — tick-gap fires but convergence is slow. |
| >30s | any | any | any | any | **High.** Promote **S1** (OS wake notifications) + **S4**; rebind path is stalling. |
| any | any | any | >30s | any | **Medium.** Promote **S5a** (pre-sleep bye-bye packet). |
| any | any | any | any | utun absent / "tun device not found" | **High.** Promote **S6** (TUN health probe + cold restart). |
| any rc!=0 on pub_rc in T4 | — | — | — | — | **Regression.** Public DNS got blocked by our stub — split-DNS isolation broken. File as bug first, don't chase solutions until triaged. |

---

## Potential solutions (apply only if a test reveals the need)

Keyed by failure mode. All optional — don't build without empirical evidence.

### S1 — OS-level sleep/wake notifications (refinement, not replacement)

**When:** T2 recovery >30s (rebind happens after wake, but too slowly).

**What:** subscribe to macOS `NSWorkspace.didWakeNotification` / IORegister
`kIOPMSystemPowerStateCapabilityCPU`, Linux `org.freedesktop.login1` `PrepareForSleep`,
Windows `WM_POWERBROADCAST`. On wake, immediately invoke the same rebind path.

**Why not default:** tick-gap already catches what they catch (Tailscale ran
`TimeJumped` for years). OS notifications are a latency improvement (ms vs up
to 5s — one ticker interval), not a correctness improvement. Ship only if
measurements show the 0-5s tick latency matters.

### S2 — Adaptive tick interval (battery tradeoff)

**When:** mobile clients ship (currently: not applicable).

**What:** poll at 5s when tunnels active, back off to 30s when idle, triggered
by actual traffic. Mirrors Tailscale #1554 resolution.

### S3 — DNS stub suspend/resume (Tailscale #17736 mitigation)

**When:** T4 reveals `.home` OR public DNS hangs on wake.

**What:** in kernel-TUN mode, before sleep (via S1 sleep notification) remove
our resolver from `scutil --dns`; after tunnel recovery, re-add. In the gap,
system DNS uses the router's resolver only — mesh names fail, public names
work. Fails closed instead of hanging.

**Complexity:** ~150 LOC. Needs sleep notification (S1) to be accurate. Risk:
if rebind fails, mesh DNS stays unregistered — need a watchdog to re-register
after any successful tunnel.

### S4 — Faster rebind retry on wake

**When:** T2 shows rebind attempted but fails (WiFi not up yet).

**What:** after a rebind returns an error, retry at 500ms for 10s before giving
up. Currently the next retry is 5s later (next tick). Modest improvement.

### S5 — Proactive peer notification (close black-hole window)

**When:** T5 shows black-hole window >30s.

**What two options:**
- **S5a — pre-sleep "bye-bye".** On sleep notification, send a single Nebula
  tunnel-close control packet to each active peer. Peer drops our tunnel
  immediately. On wake, fresh handshake. Requires reliable sleep notification.
- **S5b — faster peer-side tunnel testing.** Lower Nebula's tunnel-test
  interval from ~15s to ~5s on the *peer* side only when we detect our tunnel
  is idle but stale. Protocol change — avoid.

Prefer S5a.

### S6 — TUN re-creation on wake

**When:** T6 reveals utun corruption after hibernate.

**What:** add a "TUN health" probe alongside tick-gap — after a long gap, try a
zero-length read from the utun fd with a short deadline. If it errors in a
way that looks like "fd invalidated," tear down Nebula and restart via
`reloadNebula()` cold-start path (already exists for the cert case).

---

## What NOT to build (until evidence demands it)

- **Power Nap handling.** macOS-specific edge case where the process runs during
  dark wake but WiFi may be unavailable. Only matters if users report it.
- **Battery optimization for wake handling (S2).** Only relevant for
  iOS/Android mobile clients which we don't ship yet.
- **Fork of Nebula's tunnel-test logic (S5b).** Protocol change. Pre-sleep
  "bye-bye" (S5a) gets most of the benefit with zero protocol risk.

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
- hopssh fixes: commit 606384b (v0.9.6), `cmd/agent/nebula.go:166-197`,
  `cmd/agent/renew.go:517-560`
- Evidence: `spike/nebula-baseline-evidence/` (30s outage survival test) — target `spike/sleep-wake-evidence/` (this plan)
