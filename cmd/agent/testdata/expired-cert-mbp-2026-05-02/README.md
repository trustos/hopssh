# Expired-cert evidence — MBP 2026-05-02 incident

Snapshot taken from the user's MBP at `192.168.23.18` on 2026-05-02 at
11:25 EEST, **before** running `sudo launchctl kickstart -k system/com.hopssh.agent`
to recover the agent. Used as test fixtures for the Phase P regression
suite (renewal-watchdog + UI-honesty fixes that ship in v0.10.79).

## What happened

The hop-agent v0.10.78 LaunchDaemon stopped emitting `[renew home]` /
`[heartbeat home]` log lines after `2026-05-01 22:59:50 EEST`. The
final pre-silence log line:

```
2026/05/01 22:59:50 [renew home] next renewal in 5h49m54s
```

scheduled the next renewal at `2026-05-02 04:49:44 EEST`. The
goroutine never woke. The cert hard-expired at `2026-05-02 06:14:27 UTC`
(`09:14:27 EEST`). Other agent goroutines (`watchNetworkChanges`,
`mesh-keepalive`, the Nebula UDP control loop) kept running. The
desktop UI showed `connected` (green) with `peersDirect=0`,
`peersRelayed=0`, and inline `lastError: certificate expired` — a
direct contradiction the user reported as a critical user-trust
bug.

Forensic root-cause hypotheses that were CHECKED against the
artifacts and REFUTED:

- "Goroutine inherited a canceled ctx" — refuted by reading
  `cmd/agent/main.go:512-525`. `inst.runCtx` is freshly derived
  before each `runCertRenewal` spawn; even a canceled ctx would
  produce a `[renew home] next renewal in ...` line at
  `cmd/agent/renew.go:404` before the `ctx.Done()` check.
- "Process crashed" — refuted by uptime (12h17m at probe time)
  and continued emission of other goroutines' log lines.
- "Network unreachable / clock skew" — refuted by `nc -zv
  hopssh.com 443` succeeding and zero clock skew vs hopssh.com's
  `Date:` header.

What's NOT refuted: `timeUntilRenewal` blocked indefinitely (cert
file read or parse hung), or a deferred `recover()` along the
call stack swallowed a panic, or some other defensive primitive
the watchdog will catch regardless of cause.

## Files

| File | Format | Sanitized? | Notes |
|---|---|---|---|
| `node.crt` | Nebula PEM (BEGIN NEBULA CERTIFICATE) | No — public cert, no private material | Expired. NotAfter `2026-05-02T06:14:27Z`. Use as fixture for "expired cert" tests. |
| `ca.crt` | Nebula PEM | No — public CA cert | The CA that signed `node.crt`. |
| `node.key` | NOT INCLUDED | n/a | Intentionally NOT copied — that's the agent's private signing key. Tests that just read `NotAfter` from the cert don't need the key; tests that need to mint a new cert for the same node should generate fresh keypair fixtures via the test harness. |
| `peers.json` | JSON | No | Last-known peer endpoint cache. `seenAt` is unix-seconds; values like `1777498938` (= 2026-04-29 21:02 UTC) prove peers were stale ~35h before probe time. |
| `nebula.yaml` | YAML | No | The Nebula instance config. Contains listen port, lighthouse, paths to `node.crt` / `ca.crt`. |
| `dns-domain` | Plaintext | No | "home" — the per-network DNS suffix. |
| `relay-state.json` | JSON | No | Relay-mode persistence state. |
| `renew-log-tail.txt` | Text | No | Last 200 matching lines from `/var/log/hop-agent.log` filtered for `renew home`, `cert auto-renewal`, `reloadNebula`, `sendmsg_x`. The "renewal goroutine stopped emitting" pattern is visible at the tail. |

## Test usage

For the Phase P regression suite shipping in v0.10.79:

- `TestEnrollmentStatus_ConnectedFalseWhenCertExpired` — load
  `node.crt` via `cert.UnmarshalCertificateFromPEM`, assert
  `enrollmentStatus(...).Connected == false` and
  `LastError == "certificate expired"`.
- `TestRenewalWatchdog_FiresAfterSilenceThreshold` — DOESN'T
  need the cert; needs only a fake `meshInstance` with a
  backdated `lastRenewalActivityAt`. The cert-expired fixture
  is for the UI-honesty test, not the watchdog test.

## Sensitivity

These files are from the user's own self-hosted production
environment. They're suitable for fixtures BECAUSE:

- The `node.key` (private signing key) is intentionally NOT
  included.
- The `node.crt` is already expired — even if leaked, an
  attacker cannot use it to impersonate the node (peers reject
  expired certs, which is precisely the behavior that
  triggered this bug).
- The `node-id`, mesh IP, and CA fingerprint are all already
  derivable from the in-network data the user owns.
- This repo (trustos/hopssh) is private; a future open-sourcing
  pass should re-evaluate.

## Recovery

After this snapshot was taken, the user ran:

```
sudo launchctl kickstart -k system/com.hopssh.agent
```

which SIGKILLed the daemon and triggered launchd to respawn it.
The fresh process boots a new `runCertRenewal` goroutine that
sees the expired cert (NotAfter < now), wakes immediately, POSTs
to `/api/renew` authenticated by the bearer token (NOT the
expired cert), and writes a fresh cert to disk. Mesh comes back
in ~30s.
