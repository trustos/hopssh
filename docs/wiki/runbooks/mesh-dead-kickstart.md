---
type: runbook
title: Mesh appears dead — kickstart launchd
status: current
last_compiled: 2026-05-03
sources:
  - cmd/agent/service.go
  - docs/wiki/concepts/macos-system-mode.md
---

# Mesh appears dead — kickstart launchd

**TL;DR.** When a system-mode agent is running but the mesh is unreachable, kick the LaunchDaemon to force an immediate cert refresh + reconnect.

## Symptom

You're hitting one or more of these:

- `hop-agent status` reports the agent is running, but `ping <peer-vpn-ip>` fails.
- Dashboard shows the node "degraded" for >5 minutes.
- Cert is expired or about to expire AND the renewal goroutine appears stuck (no `[renew home] cert OK` lines in `/var/log/hop-agent.log` within the last few minutes).
- The Phase P2 stuck-data-plane watchdog already fired and dropped a forensic file at `/etc/hop-agent/<network>/stuck-state-*.txt`, but recovery didn't fully take.

## Cause (in one sentence)

Either the renewal goroutine has silently died (a class of bug Phase P2 is designed to detect) or a transient network event left an internal Nebula state cache wedged. Restarting the daemon refreshes the cert from the server (authenticating via bearer token, NOT the expired cert) and rebuilds all internal state.

## Action

```bash
sudo launchctl kickstart -k system/com.hopssh.agent
```

Step by step:

1. **Capture forensics first** if the watchdog hasn't already. Copy `/etc/hop-agent/<network>/stuck-state-*.txt` somewhere safe — this file disappears on a clean restart.
2. **Run the kickstart command** (above). The `-k` flag kills + restarts atomically.
3. **Verify recovery within ~10 seconds:**
   ```bash
   tail -f /var/log/hop-agent.log
   # expect: "starting up... v0.10.X" + "cert OK" + peer handshakes
   ```
4. **Confirm the mesh:** `ping <peer-vpn-ip>` should return within 50ms (LAN) or 200ms (cellular).

## When NOT to run this

- **Don't kickstart during a known control-plane rollout window.** If you just pushed a release, the lighthouse may be intentionally unreachable for ~5–10 min — the watchdog will recover automatically (see [[incidents/2026-05-02-mbp-watchdog-deploy-bounce]]). Kickstarting unnecessarily destroys forensic state.
- **Don't kickstart if you haven't checked the renewal log first.** If `cert OK` lines are firing every 60s, the renewal goroutine is healthy and your problem is probably elsewhere (CGNAT flow expiry, peer-side issue, network change you haven't noticed).

## Side effects

- All open mesh tunnels drop and re-handshake. Long-lived TCP connections inside the mesh (e.g. SSH sessions, Screen Sharing) WILL break. Tools with auto-reconnect (mosh, web SSH) recover; raw TCP does not.
- Forensic dump files in `/etc/hop-agent/<network>/stuck-state-*.txt` are NOT deleted by kickstart — they persist for post-hoc review.
- The cert is refreshed from the server. If the server is unreachable, kickstart will leave the agent in a retry loop instead of recovered.

## Backlinks

- [[concepts/macos-system-mode]]
- [[concepts/watchdog]]
- [[incidents/2026-05-02-mbp-watchdog-deploy-bounce]]
