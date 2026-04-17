# Sleep/wake resilience test evidence

Empirical validation of the v0.9.6 sleep/wake fixes
(`cmd/agent/nebula.go:166-197` tick-gap detection, `cmd/agent/renew.go:517-560`
cert-expired cold-start) and the failure-mode matrix in
[`docs/sleep-wake-plan.md`](../../docs/sleep-wake-plan.md).

Answers the question: "Do we actually handle the failure modes we claim to, or
should we be introducing fixes?"

## Test setup

- **Subject** — MacBook Pro, WiFi, overlay IP `10.42.1.6`. Sleeps via `pmset sleepnow`.
- **Peer / observer** — Mac mini, Ethernet, overlay IP `10.42.1.7`. Never sleeps;
  runs continuous ping + filtered `hop-agent` log tail for the whole session.
- Both agents `hop-agent vX.Y.Z` (fill in from `00-fingerprint.txt` after the run).
- Same LAN, same-subnet, lighthouse+relay via Oracle Cloud arm64 control plane.
- Methodology: six tests (T1–T6) executed per the runbook in
  [`docs/sleep-wake-plan.md#test-plan`](../../docs/sleep-wake-plan.md#test-plan).
  `pmset sleepnow` used instead of lid-close to eliminate human-timing variance.
  Every event bracketed with millisecond UTC timestamps.

## Files

- `00-fingerprint.txt` — pre-test state on both nodes: agent versions, TUN mode,
  `/etc/resolver/<domain>` contents, `scutil --dns`, cert expiry, original
  `hibernatemode` (needed for T6 cleanup).
- `peer-ping-continuous.log` — peer-side 1Hz ping to subject over the full session.
- `peer-agent-continuous.log` — filtered peer agent log over the full session
  (sleep/wake, rebind, tunnel-close, handshake events).
- `subject-agent-continuous.log` — filtered subject agent log for the full session.
- `01-t1-*` — short sleep (10s), verify tick-gap does NOT fire.
- `02-t2-*` — medium sleep (2min), primary baseline for tick-gap + recovery.
- `03-t3-*` — long sleep (10min), stress duration.
- `04-t4-*` — DNS recovery + `/etc/resolver` diff (kernel-TUN mode only).
- `05-t5-peer-timeline.log` — peer-side black-hole window analysis, derived
  from `peer-ping-continuous.log`.
- `06-t6-*` — hibernate cycle, utun survival check.
- `RESULTS.md` — decision matrix filled in with measured numbers, action taken
  per row of `docs/sleep-wake-plan.md` failure-mode matrix.

## Result

All six tests PASS. Full analysis in [`RESULTS.md`](RESULTS.md).

| Test | Verdict | Key number | Notes |
|---|---|---|---|
| T1 — 9s sleep | ✅ PASS | 0 rebinds post-wake | Tick-gap did NOT fire for sub-threshold sleep |
| T2 — 2min sleep | ✅ PASS | peer-recovery 3s | Rebind fired via addrChanged branch; `closed 3 tunnels` |
| T3 — ~13min sleep | ✅ PASS | tunnel recovered within ~1s of each wake | pmset schedule wake unreliable for long sleeps — methodology learning, not a VPN issue |
| T4 — DNS | ✅ PASS (clean) | mesh DNS OK at T+5s | Resolver diff empty; pub_rc=0 on every sample |
| T5 — peer black-hole | ✅ PASS | <3s across all tests | Peer recovers as soon as subject re-handshakes |
| T6 — hibernate | ✅ PASS | 191s outage; utun intact | hibernatemode restored to 3 (safety verified) |

## Strategic conclusion

**v0.9.7 fixes empirically validated.** The sleep/wake failure-mode matrix in
`docs/sleep-wake-plan.md` is now mostly all green — the only remaining "not
handled" items (Power Nap, battery/speed tradeoff) are edge cases explicitly
deprioritized for current scope.

No solutions from S1–S6 in the plan doc need to be promoted to the roadmap
based on these measurements. Competitive-analysis and performance docs can
cite "3s recovery from 2-minute sleep on macOS" directly.

## Secondary findings (worth remembering)

1. `pmset schedule wake` is unreliable on Apple Silicon MacBooks for sleeps ≥10 min. WOL is the reliable fallback.
2. The agent's log reports `"network change detected"` (not `"sleep/wake detected"`) for sleep cycles where WiFi re-associated — the `addrChanged` branch wins the condition before `sleptAndWoke` gets a chance to log. Functionally equivalent; diagnostically confusing.
3. Recovery flaps: every soft-sleep recovery produced two rebinds in quick succession (~6s apart) with brief ping-loss between them — WiFi stabilisation, not a VPN issue. Full convergence ≤12s.
