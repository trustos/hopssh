# hopssh wiki — open questions

Cross-cutting unresolved items. Acts as a handoff state machine across sessions: when a session ends with "next session should do X", file it here, not in chat.

Per-incident open items live on the incident page itself (`## Open` section). This file is for items that span multiple pages or that don't have a natural home yet.

Format:

```
## YYYY-MM-DD — short title

What's open. Why it matters. Where to start.

Source / context: link to wiki page or external URL.
```

Move closed items to a "Resolved" section at the bottom rather than deleting — the resolution rationale is itself useful.

---

## Open

## 2026-05-07 — which vendor Nebula call wedged in the May 7 watcher incident

The watchNetworkChanges goroutine deadlocked at `cmd/agent/nebula.go:272-273` — either inside `ctrl.RebindUDPServer()` or `ctrl.CloseAllTunnels(true)`. We couldn't determine which from logs alone (no CRITICAL/PANICKED, no goroutine dump pre-fix). Phase DD's watchdog will produce a forensic goroutine dump on the next occurrence (`<configDir>/<name>/watcher-stuck-<ts>.txt`) — that dump will pinpoint the lock contention and inform whether to file an upstream Nebula issue or extend our vendor patches. **Action**: when the next watcher-stuck dump appears in the wild, walk the goroutine stack and identify the held mutex.

Source / context: [[incidents/2026-05-07-mbp-watcher-wedge]]

## 2026-05-07 — DNS resolution flap during MBP network changes

`dial udp4: lookup hopssh.com: no such host` fires intermittently during network-change events on MBP (observed at 17:18:56, 18:00:45 today; also in earlier sessions on 2026-05-05, 2026-05-06). Heartbeat goroutine recovers within seconds (separate goroutine, separate DNS resolution). Could be Go runtime cgo_resolver cache invalidation during interface flap, or macOS DNSExtensionConfig transitioning. Unrelated to the wedge itself (DNS error returns cleanly), but worth understanding if it correlates with the May 7 wedge timing or recurs as a primary failure mode. **Action**: low priority; investigate if a DNS-flap-driven incident appears.

Source / context: [[incidents/2026-05-07-mbp-watcher-wedge]] § "Open questions"

---

## Resolved

## 2026-05-09 — Linux desktop client stuck onboarding + "Load failed" on Connect (resolved 2026-05-10)

Resolved in v0.11.19 (commit 10ff0c9). Root cause: `cmd/agent/main.go` skipped `c.Start` on the no-enrollments branch (fresh installs), but `client.StartLocalAPI` was always called — the local API's auto-connect handler then panicked on nil `c.runCtx` during `context.WithCancel`. Phase NN regression: pre-extraction the equivalent closure used the always-live `renewCtx`. Fix is two-layer (always Start before StartLocalAPI; defensive nil guard in connect). Live-verified on the aarch64 Ubuntu VM. See [[incidents/2026-05-09-linux-desktop-stuck-onboarding]] § Resolution for the captured panic + lessons.
