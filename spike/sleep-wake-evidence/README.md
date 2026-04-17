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

_Fill in after the run. Template:_

| Test | Pass/Fail | Key number | Notes |
|---|---|---|---|
| T1 — short sleep | ? | — | Tick-gap did/did-not fire (must not fire at 10s) |
| T2 — 2min sleep | ? | self-recovery: Ns | Tick-gap value, tunnels closed |
| T3 — 10min sleep | ? | self-recovery: Ns | Any anomalies vs T2? |
| T4 — DNS recovery | ? / N/A | mesh dig OK at: Ns | Resolver diff empty? pub_rc always 0? |
| T5 — peer black-hole | ? | window: Ns | From peer-ping gap analysis |
| T6 — hibernate | ? | self-recovery: Ns | utun survived? hibernatemode restored? |

## Strategic conclusion

_Fill in after the run. Two templates, pick one:_

**If all tests pass:** v0.9.6 fixes are empirically validated against the
failure-mode matrix. No solutions (S1–S6) required. Update competitive-analysis
docs with measured recovery numbers.

**If some tests fail:** promote the corresponding solution(s) in
`docs/sleep-wake-plan.md` §"Potential solutions" to the roadmap. Reference
this evidence directory as justification.
