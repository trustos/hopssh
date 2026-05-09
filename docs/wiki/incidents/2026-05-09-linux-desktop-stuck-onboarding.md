---
type: incident
title: 2026-05-09 — Linux desktop client stuck onboarding + "Load failed" on Connect
status: resolved
last_compiled: 2026-05-10
sources:
  - cmd/agent/main.go
  - internal/client/api.go
  - internal/client/client.go
  - internal/client/local_api.go
shipped_in: v0.11.19 (commit 10ff0c9)
---

# 2026-05-09 — Linux desktop client stuck onboarding + "Load failed" on Connect

**TL;DR.** First post-Stages-5+6 user-test of the Linux desktop client (`hopssh-linux-aarch64.deb` on an aarch64 Ubuntu VM) surfaces two symptoms: (1) onboarding stays at step 2 ("We opened a browser tab to approve this Mac" — older copy, fixed in v0.11.16) without advancing after the user manually approves the device in the browser, and (2) clicking the manual Connect button later in the flow surfaces "Load failed" — a JS-side `TypeError: Failed to fetch` from `local-api.ts:94`, meaning the bundled hop-agent's loopback HTTP listener became unreachable mid-request. Disk state confirms enrollment IS persisted (`enrollments.json` has the `home` entry; cert files at `~/.config/hopssh/home/`). Process state at user's last screenshot: `hopssh-desktop` (PID 154043) + `hop-agent` child (PID 154089) BOTH alive. journalctl excerpt shows a goroutine stack frame leading to `(*Client).connect` at `internal/client/client.go:87` (the `_, err := try()` line in the 4-attempt retry loop) — could be a panic stack from a Go runtime crash OR a goroutine pprof dump from the v0.10.96 watcher watchdog. **Root cause not isolated yet** — needs the full panic message (line above the stack frames) from journalctl, which the diagnostic SSH session was cut short before precompact.

## Symptoms

| Surface | What the user sees | What's true on disk / process |
|---|---|---|
| Onboarding step 2 | "We opened a browser tab to approve this Mac." (pre-v0.11.16 copy — already fixed in 226a3e2) — and the page never advances even after the user opens the URL manually + approves | Device-flow `device_codes` row does get authorized server-side (the user said "tried to connect" implying they got past it on retry); enrollments.json on disk has the home network entry; auto-connect path didn't transition the UI to "completing" |
| Manual Connect | Toast: "Load failed" | hop-agent child is alive (PID 154089) but the .app's fetch to `127.0.0.1:<port>/local/connect` returned TypeError — listener not responding |
| journalctl `[agent]` lines | Go stack frames pointing at `internal/client/client.go:87` and `internal/client/local_api.go:1146` | Either a panic during `(*Client).connect`'s `try()` body OR a watchdog forensic dump |

## What we know

- The Linux build matrix shipped in v0.11.13/14/15 covers x86_64 + aarch64 for `.deb` + `.rpm` (skips AppImage on aarch64 since Tauri's `appimagetool` is x86_64-only — v0.11.14 hotfix).
- `resolve_agent_path` was extended in v0.11.15 (commit fc7a07a) to find the sidecar at `<install-dir>/../lib/hopssh/binaries/hop-agent` for Linux .deb installs and `<install-dir>/hop-agent.exe` for Windows. The user's report came AFTER v0.11.15 + v0.11.16 + v0.11.17 deployments, so resolve_agent_path is the right shape on Linux.
- The bundled child does spawn: PID 154089 confirms hop-agent is running. So the launch path works end-to-end.
- The .app's local-api.ts has NO fetch timeout — `Load failed` strictly means the kernel-level connect failed (RST or unreachable port). Not a JS-side abort.
- Auto-connect IS wired into the device-flow poll path (`internal/client/local_api.go::handlers/device-flow/poll` calls `s.client.connect(name)` on enrollment success). If auto-connect succeeded, the UI would have advanced to step 3 / done.
- The watcher watchdog shipped in v0.10.96 (Phase DD) writes goroutine pprof dumps to `<configDir>/<name>/watcher-stuck-<ts>.txt` — those would NOT be in journalctl. So the stack frames in journalctl are most likely from a panic, not the watchdog.

## What we don't know

- The full panic message (the line above the stack frames in journalctl). Required to identify the root cause. SSH access to the VM was lost at precompact.
- Whether the Linux build has a CGO-enabled or CGO-disabled build (would affect Nebula UDP socket batch syscalls + clipboard stub fallthrough).
- Whether `aarch64` Linux specifically has a Nebula vendor-patch incompatibility we haven't tested. All three watchdog tests pass on `linux/amd64` in CI; the aarch64 Linux runner is `ubuntu-22.04-arm` — same kernel family, different arch.

## Hypotheses (ranked)

1. **Panic in startInstance during the auto-connect path on enrollment.** A crash here would terminate hop-agent; Tauri's `watch_stdout` would emit `agent-exited`; the .app's subsequent fetches to the dead loopback port would all return `TypeError: Failed to fetch`. The fact that the user's snapshot showed PID 154089 alive means the agent must have RESPAWNED — but Tauri's spawn_and_watch is single-shot (no auto-respawn loop on Linux). Could the user have manually relaunched the .app between Onboarding stuck → Connect failed? Probable.

2. **TUN device or UDP port collision from a prior crashed instance.** The 4-attempt retry budget in `client.go::try` includes `waitForUDPPortFreeFn(listenPort, 3s)` + `waitForTUNDeviceFreeFn(devName, 3s)` per attempt. If a previous `hop-agent` (whether bundled or accidentally run as `hop-agent serve` from a terminal) is holding `4242/udp` or the `hop-home` TUN device, all 4 attempts fail. The fallthrough returns the v0.10.X "another hop-agent is using the network port" error — NOT "Load failed". So this hypothesis doesn't match the symptom unless the agent ALSO crashed for a different reason.

3. **Linux kernel TUN bring-up requires CAP_NET_ADMIN; running unprivileged falls back to userspace gvisor — but if `tunMode == "kernel"` was persisted and the fallback path has a regression on aarch64.** The userspace fallback in `lifecycle.go::startMesh` is "try kernel, fall through to userspace". On non-root Linux without CAP_NET_ADMIN, kernel TUN fails but userspace SHOULD succeed. If userspace ALSO fails on aarch64 specifically, `startMesh` returns nil, `client.go:197 startMesh returned nil` is logged, and `startInstance` falls back to OS-stack listener — no crash. Doesn't match "Load failed" either.

4. **Tauri sidecar pickup mismatch on Linux .deb.** v0.11.15's `resolve_agent_path` adds `<exe-parent>/../lib/hopssh/binaries/hop-agent` for Linux .deb. If the user's install layout is different (e.g. .rpm puts binaries in a different path; or AppImage has its own layout), `resolve_agent_path` could return None and the .app would surface "could not locate hop-agent". But the user said the agent IS running, so this isn't the issue today.

## Next investigation steps

1. SSH to the VM (credentials in `e2e-connections` repo) and grep journalctl for the panic message:
   ```bash
   journalctl --user -n 500 --no-pager | grep -B 3 -A 30 "panic\|runtime error\|fatal error"
   ```
2. Check `~/.config/hopssh/home/` for any forensic dump files (`stuck-state-*.txt`, `watcher-stuck-*.txt`, `renewal-stuck-*.txt`).
3. Check the .app's stderr capture (if Tauri logs are persisted to disk).
4. If the panic message doesn't show, run `hop-agent serve` from a terminal (not via .app) and reproduce the connect — the stderr will be visible directly.
5. Confirm whether the user is on an x86_64 VM running aarch64 binary (the original Exec format error from v0.11.13 era) or actually on aarch64. `uname -m` on the VM.

## Resolution (v0.11.19, commit 10ff0c9)

**Root cause confirmed via journalctl on the aarch64 Ubuntu VM:**

```
http: panic serving 127.0.0.1:46684: cannot create context from nil parent
context.WithCancel({0x0?, 0x0?})
        /home/runner/.../src/context/context.go:241 +0xc0
github.com/trustos/hopssh/internal/client.(*Client).startInstance(0x...,{0x0, 0x0}, 0x...)
        internal/client/client.go:145 +0xbc
github.com/trustos/hopssh/internal/client.(*Client).connect.func1(...)
        internal/client/client.go:57
github.com/trustos/hopssh/internal/client.(*localAPIServer).handleEnrollDeviceFlowPoll(...)
        internal/client/local_api.go:903 +0xf48
```

The local API's auto-connect path (handleEnrollDeviceFlowPoll → s.client.connect → c.startInstance) reads `c.runCtx`, which is assigned by `Client.Start`. Pre-fix, `cmd/agent/main.go` only called `c.Start` when enrollments existed (line 178 in the `else` branch). On fresh installs (zero enrollments) it skipped Start entirely — but `client.StartLocalAPI` was always called at line 157, exposing connect/enroll endpoints whose handlers needed runCtx. When the user's device-flow enrollment landed via the loopback API and triggered auto-connect, `context.WithCancel(c.runCtx)` panicked with `cannot create context from nil parent`.

**Phase NN regression** — pre-extraction the local-API's connect closure captured the agent-wide `renewCtx` from `runServe`, always live regardless of enrollment state. Post-NN, `c.runCtx` replaced it but Start() is what assigns it.

**Two-layer fix** (commit 10ff0c9):

1. **`cmd/agent/main.go`** — call `c.Start(shutdownCtx)` UNCONDITIONALLY before `StartLocalAPI`. Start is idempotent (CompareAndSwap guard); with zero enrollments it just sets runCtx + runs `migrateListenPorts` on an empty registry. The `else` branch in the listener-decision if/else was removed (Start now handles bringing up enrollments).
2. **`internal/client/client.go::(*Client).connect`** — defensive nil-runCtx guard returns "client not started" error instead of panicking. Any future regression that lets connect run before Start surfaces as a clean error instead of a goroutine crash bouncing through `net/http`'s panic recovery.

**Regression tripwires:**

- `cmd/agent/main_test.go::TestRunServe_StartBeforeStartLocalAPI` — source-scan asserts `c.Start` appears before `StartLocalAPI` in main.go.
- `internal/client/local_api_lifecycle_test.go::TestConnect_NilRunCtxReturnsErrorNotPanic` — exercises the nil-runCtx path and asserts graceful error, not panic.

**Live verification on the aarch64 Ubuntu VM** (192.168.23.232): fresh-install `hop-agent v0.11.19-dev` (replaced `/usr/lib/hopssh/binaries/hop-agent`, wiped `~/.config/hopssh/`) accepts `/local/status`, `/local/connect?enrollment=does-not-exist`, and `/local/enroll/device-flow/start` without crashing. Process stays alive across all calls; no Go panic in journalctl.

**Architectural lesson.** Any agent state that exposes a network surface BEFORE its full lifecycle is initialized is a panic-trap: handlers will get called and reach not-yet-set fields. When extracting a struct from a runServe-like function (Phase NN), audit every exported method's transitively-reachable `c.<field>` for "is this assigned by NewClient or by Start?" — and force the right ordering at every entry point. The `c.runCtx` field passes both as a nil-zero-value AND through reflection-friendly tests, but only fails when a real handler reaches it. Two-layer defense (correct ordering + defensive nil guard) is the right model for this class.

## Cross-references

- v0.11.13 first cross-platform desktop ship: commit 7cf6c9f
- v0.11.14 aarch64 Linux build matrix fix: commit 5add4d6
- v0.11.15 resolve_agent_path Linux/Windows extension: commit fc7a07a
- v0.11.16 + v0.11.17 desktop "this Mac" / "your Mac" copy cleanup: commits 226a3e2, a771bc8
- Mirror open question in `docs/wiki/open-questions.md`
