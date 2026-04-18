# Windows ConPTY shell bug — reproduction + fix evidence

**Date:** 2026-04-18
**Agent release:** v0.9.8 (broken) → v0.9.9 (fixed)
**Platform:** Windows 11 Home 25H2 ARM64, UTM VM

## Bug

Browser-based web terminal rendered blank on Windows agent only (Mac mini
and MacBook agents worked fine). No prompt visible, no response to typed
commands. Other agent endpoints (health, exec, port forward) worked.

## Harness

`spike/windows-shell-test/` — standalone Go WebSocket client for the
agent's `/shell` endpoint. Eliminated the browser → control plane → mesh
→ agent iteration loop from the debug cycle. Run it from Mac mini over
the mesh directly at the Windows agent.

## Evidence

| File | What it shows |
|---|---|
| `run1-baseline.log` | Broken state. 4 frames received (focus-mode, clear, chcp title, admin title). Prompt and echo BOTH absent from pipe. |
| `run2-freeconsole.log` | With `FreeConsole()` added at startup — no change. Same 5 frames, still no prompt. Confirmed the inherited console is not the route, even though the agent DID have one. |
| `run3-usestdhandles.log` | With `STARTF_USESTDHANDLES` added to StartupInfoEx.Flags. **6 frames, prompt visible, echo of `echo HOP_TEST_OK_44` works, output `HOP_TEST_OK_44` streams back.** |
| `run4-final.log` | Post-cleanup (debug logging removed from agent). 18 frames for a `dir\r\n` round-trip — full directory listing streams through the pipe, ends back at prompt. |

## Root cause

Without `STARTF_USESTDHANDLES` set on the child's StartupInfoEx, Windows
routes cmd.exe's stdio through the parent process's inherited console
handles instead of the PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE pipes — even
though the pseudoconsole attribute is set. Setup ANSI bytes (written via
console APIs) still reach the pipe, but ordinary stdout writes (including
cmd.exe's prompt string) leak to the parent. The parent in our case was
the SSH-inherited chain `ssh → cmd.exe → powershell → hop-agent.exe`; the
prompt text printed directly into the agent's stderr stream (visible
inline in the agent's log file).

The fix: set `Flags = windows.STARTF_USESTDHANDLES` on StartupInfoEx,
leave `StdInput/StdOutput/StdError` at zero. The pseudoconsole attribute
substitutes the pipes during CreateProcess so the zero values never get
consulted. Documented in CLAUDE.md Windows Platform section. Matches the
[UserExistsError/conpty](https://github.com/UserExistsError/conpty)
library pattern.

## Timeline

| Time (UTC-) | Event |
|---|---|
| 10:06 | First harness run — 4 frames, no prompt. Pattern matches browser. |
| 10:10 | Tried FreeConsole — no help. Ruled out inherited-console-leak as the SOLE cause. |
| 10:15 | Cloned UE-conpty, compared vs hopssh. Only functional delta: STARTF_USESTDHANDLES. |
| 10:18 | Added flag, rebuilt, redeployed — prompt + echo work. |
| 10:20 | Cleaned debug logging, rebuilt, final harness run confirms fix. |

## Why this wasn't found sooner

Browser-based testing makes every iteration cost an SSH rebuild + deploy
+ kill + restart + open browser + navigate + click. ~90s per cycle minimum.
With the harness, the loop is <5s per cycle and the output is machine-
inspectable (hex + ASCII + timings). The fix took ~15 minutes once the
harness was in place; the prior hour+ of browser-driven attempts at this
bug produced no conclusive data, only guesses.
