---
type: incident
title: 2026-05-02 — MBP work-network watchdog tripped during v0.10.84 deploy bounce
status: resolved
last_compiled: 2026-05-03
sources:
  - cmd/agent/keepalive.go
  - cmd/agent/watchdog_test.go
  - raw/2026-05-02-deploy-bounce/agent-log.txt
  - raw/2026-05-02-deploy-bounce/stuck-state-20260502T204038Z.txt
---

# 2026-05-02 — MBP work-network watchdog tripped during v0.10.84 deploy bounce

**TL;DR.** A scheduled control-plane container rollout made the work-network lighthouse UDP-unreachable for ~7 minutes. The agent's stuck-data-plane watchdog (Phase P2) fired correctly, captured a forensic dump, and auto-restarted the work mesh instance. Mesh recovered in seconds. **No code change** — the architecture worked as designed; the user-visible signal was the dashboard briefly showing "degraded".

## Timeline

| Time (local) | Event | Source |
|---|---|---|
| 23:09:10 | MBP enters Clamshell Sleep (lid closed) | pmset |
| 23:14:48 | Real Wake (lid opened) | pmset |
| 23:14–23:30 | Both networks operating normally; Phase S 60s ticker survives sleep correctly | agent log |
| ~23:30:00 | v0.10.84 pushed; Nomad job updated; control-plane container begins rolling | git history |
| 23:30:02 | `Close tunnel received... vpnAddrs=[10.42.2.1]` — work lighthouse drops | agent log |
| 23:30:05–16 | Work-lighthouse handshakes timing out | agent log |
| 23:30:18 | `[heartbeat home] failed... HTTP 404` (control-plane mid-rollout) | agent log |
| 23:30 → 23:40 | Work network keepalive failing all probes | agent log |
| **23:40:38** | **WATCHDOG fires** — forensic dump at `/etc/hop-agent/work/stuck-state-20260502T204038Z.txt`; `restartFn` invoked on work mesh instance | agent log |
| 23:40:46 | Work network back online; renewal poll resumes | agent log |

## Symptom

Dashboard pill flipped to "degraded" for the work network for ~30 seconds around 23:40. User screenshot captured the transition window. Both networks showed `connected: true` 15 minutes later when probed live.

## Investigation

Hypotheses ruled out:

- **Phase S regression** — `[renew home] cert OK ... next poll in 1m0s` log lines fired every 60s through Clamshell Sleep + DarkWake transitions. Phase S ticker working as designed.
- **Sleep/wake recovery bug** — `watchNetworkChanges` fired correctly on wake at 23:14:48; Nebula tunnel re-handshake completed within seconds.
- **Genuine peer outage** — peer hosts were online, just couldn't reach the lighthouse.

Confirmed root cause via the forensic dump (`stuck-state-20260502T204038Z.txt`): `keepalive cycle: 3, consecutive stuck cycles: 3 (threshold 3)`, full goroutine pprof attached. The watchdog architecture (Phase P2, shipped v0.10.36) caught a real 7-minute outage and recovered.

## Root cause

Two layers, both correct behavior:

1. **The deploy bounce caused real degradation.** The work network's lighthouse (port 42002) was UDP-unreachable for ~7 minutes during the container swap. Home network (port 42001) was less affected — different lighthouse instance, recovered without restart.
2. **The watchdog responded as designed.** 3 consecutive stuck cycles (270s of failure) crossed the threshold; `restartFn` ran; new container came up; mesh reconnected.

## Resolution

No code change needed. The architecture worked end-to-end:
- Detected the failure via per-network keepalive probes.
- Captured forensics (goroutine dump + cycle counters) for post-hoc review.
- Auto-restarted the affected mesh instance.
- Recovered in ~8 seconds after the new container came up.

## Lessons

- **The watchdog's forensic-dump output is the right post-mortem artifact.** Without it, this would have been a "huh, the dashboard blipped" with no way to verify what happened. With it, root cause was clear in 5 minutes.
- **Per-network keepalive is the right granularity.** Home and work watchdogs are independent — only work tripped. A single agent-wide watchdog would have falsely flagged home too.
- **Deploy-bounce noise is a quality-of-life issue, not a correctness issue.** The system correctly recovers; the forensic dump is overkill for a known-rollout window. See "Open" below.

## Open

**Optional polish: silence the watchdog during known control-plane bounce windows.** When the agent observes HTTP 404/502/503 from `/api/heartbeat` (= server is rolling), suppress the keepalive `STUCK` counter for ~3 minutes. Prevents forensic-dump churn during routine deploys; doesn't affect outage detection (real outages don't generate HTTP 5xx, they generate `context deadline exceeded`).

Risk: misses a real outage that coincides with a 5xx. Mitigation: 3-min grace is shorter than cert-expiry window; net win.

Not blocking; can fold into a future minor release.

## Backlinks

- [[concepts/watchdog]]
- [[concepts/cert-renewal]]
- [[entities/mbp]]
- [[runbooks/mesh-dead-kickstart]]
