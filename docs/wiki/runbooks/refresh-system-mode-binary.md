---
type: runbook
title: System-mode hop-agent binary lags behind .app — refresh manually
status: current
last_compiled: 2026-05-03
sources:
  - cmd/agent/service.go
---

# System-mode hop-agent binary lags behind .app — refresh manually

**TL;DR.** When the desktop `.app` updates, the system-mode `/usr/local/bin/hop-agent` binary does NOT auto-refresh. Manually copy + kickstart to bring them in sync.

## Symptom

- Desktop UI shows version vX.Y.Z, but `hop-agent --version` (system path) reports vX.Y.W (older).
- Status panel shows "TUN: userspace" even though you're on a host that should auto-upgrade to kernel TUN at LaunchDaemon startup.
- A bug fixed in vX.Y.Z is still firing in production logs from the system-mode agent.

## Cause (in one sentence)

Phase J shipped the `.app` self-update flow, but did not extend it to refresh the binary at `/usr/local/bin/hop-agent` that the LaunchDaemon executes — the LaunchDaemon keeps running the old binary indefinitely. (Tracked as a known gap in [[concepts/macos-system-mode]].)

## Action

```bash
sudo cp /Applications/hopssh.app/Contents/Resources/binaries/hop-agent /usr/local/bin/hop-agent
sudo launchctl kickstart -k system/com.hopssh.agent
```

Step by step:

1. **Verify the .app has the newer binary:**
   ```bash
   /Applications/hopssh.app/Contents/Resources/binaries/hop-agent --version
   /usr/local/bin/hop-agent --version
   ```
2. **Copy** (requires sudo because `/usr/local/bin/` is root-owned).
3. **Kickstart the LaunchDaemon** to pick up the new binary. See [[runbooks/mesh-dead-kickstart]] for the kickstart side effects.
4. **Verify:** `/usr/local/bin/hop-agent --version` should now match the .app.

## When NOT to run this

- If the version mismatch is intentional (rolling back the system-mode agent for testing while the .app stays on the new version).
- During a known control-plane bounce window; wait for the auto-recovery first.

## Side effects

- Every mesh tunnel drops + re-handshakes (kickstart consequence).
- Long-lived TCP connections inside the mesh break.

## Backlinks

- [[concepts/macos-system-mode]]
- [[runbooks/mesh-dead-kickstart]]
