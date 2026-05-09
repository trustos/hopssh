---
type: phase
title: Phase NN — internal/client substrate extraction
status: shipped
last_compiled: 2026-05-09
ship_version: v0.11.5
ship_date: 2026-05-09
sources:
  - internal/client/api.go
  - internal/client/client.go
  - internal/client/api_constraints_test.go
  - cmd/agent/main.go (post-extraction thin shell)
  - CLAUDE.md Discovery Log § "Phase NN — internal/client/ substrate extraction"
  - docs/wiki/log.md 2026-05-09 entry
---

# Phase NN — internal/client substrate extraction (v0.11.5)

## TL;DR

Extracted ~22k LOC of client-lifecycle Go code (89 files) from `cmd/agent/` (which was `package main`) into a new `internal/client/` package with a gomobile + Rust-FFI compatible public API. `cmd/agent/` reduced to a 9-file thin shell whose `runServe()` constructs a `*client.Client` and wires HTTP via an `InstanceHTTPHook`. Unblocks NN+1 (gomobile binding), NN+2 (iOS NEPacketTunnelProvider), NN+3 (Android VpnService) — all three were paper-blocked because Go cannot import `package main`.

## What was blocked

Both [[../decisions/client-ios-architecture]] and [[../decisions/client-android-architecture]] flagged `internal/client/` as a required substrate that didn't exist. The rationale: the gomobile toolchain (and any future Rust-FFI binding) needs a regular Go package with constrained types in its public API surface. `cmd/agent/main.go` couldn't satisfy either constraint while still being a binary entry point.

## What shipped

| Surface | Detail |
|---|---|
| Public API types | `Client`, `Config`, `EnrollOptions`, `Snapshot`, `EnrollmentSummary`, `PeerInfo`, `Event`, `EventCallback`, `SubscriptionID`, `InstanceHTTPHook`, `MeshService` |
| Public methods on `*Client` | `NewClient`, `Start`, `Stop`, `Connect`, `Disconnect`, `Enroll`, `Leave`, `Status`, `Peers`, `ForceRenew`, `Subscribe`, `SubscribeCallback`, `Unsubscribe`, `SetInstanceHTTPHook`, `RegisterDebugListener`, `EnrollmentNames` |
| Exported helpers (cmd/agent calls) | `MigrateLegacyLayout`, `ResolveConfigDir`, `RunMirrorChownSelfHeal`, `SvcIntegrateIfNeeded`, `CleanupOldBinary`, `StartPprofIfRequested`, `StartLocalAPI`, `RunHelp`/`RunInfo`/`RunEnroll`/`RunStatus`/`RunLeave`/`RunClientJoin`/`RunAgentInstall`/`RunAgentUninstall`/`RunAgentUpdate`/`RunRestart`/`RunStop` |
| FFI tripwires | `internal/client/api_constraints_test.go` — three reflection tripwires (no `chan`/`func`/`interface{}` in exported struct fields) + a fourth `gomobile bind` smoke that skips when gomobile isn't installed |
| Test override | unexported `connectOverride`/`disconnectOverride` fields on `*Client` let lifecycle tests stub the connect path without spinning up real mesh state — FFI-safe (unexported fields aren't bound) |

## What stayed in cmd/agent

The thin shell — 9 files:

- `main.go` (~230 LOC, was 933) — flag parsing + CLI subcommand dispatch + `runServe` wiring
- `proxy.go`, `proxy_test.go` — HTTP proxy handler attached to the per-instance mux
- `shell_unix.go`, `shell_windows.go`, `shell_unix_test.go` — PTY / WebSocket terminal handler attached to the per-instance mux
- `migration.go` — `hop-agent migration` QUIC-probe CLI subcommand (uses `internal/quictransport` directly)
- `main_test.go` — auth middleware + handler tests

Plus an `agentHTTPHook` adapter that implements `client.InstanceHTTPHook` by wrapping the agent mux with bearer-token auth. Mobile callers pass `nil` for the hook and skip the per-instance HTTP listener entirely (deadcode-eliminated in gomobile bindings).

## Critical invariants preserved

- **`restartFn` closure pattern** — each `meshInstance.restartFn` now closes over `c.connect(name)` instead of the runServe-local `connectFn(name)`. Recursion-via-method semantics identical.
- **Three watchdogs** (renewal silent-death + stuck data-plane + watchNetworkChanges-wedge) — all spawned per-instance with stamps + cooldowns + `restartFn` binding intact.
- **Per-instance `runCtx`** — cancelling one enrollment doesn't cancel goroutines for other enrollments.
- **Vendor patches** — no `go mod vendor` invocation during the extraction; just `git mv`. Vendored Nebula is untouched; patches still apply.

## Pragmatic pivot (lesson)

The original plan said `local_api.go` (1,215 LOC, the desktop-shell HTTP server) should stay in `cmd/agent` and its handlers should be rewritten to call the public `*client.Client` API. In practice, the depth of internal-type coupling (~30 references to `*enrollmentRegistry` / `*instanceRegistry` / `*meshInstance` from handlers) made that a 3-5× larger refactor than the move-into-package alternative.

Pivoted: moved `local_api.go` into `internal/client/` too and added a thin exported `StartLocalAPI(ctx, *Client)` entrypoint. Mobile gomobile bindings dead-code-eliminate the unused HTTP server, so the FFI surface stays clean.

**Generalizable rule:** when a planned refactor's "stay" pile depends on the "move" pile's internal types more than the spec assumed, audit the call-site count BEFORE committing to a separation. Sometimes "move it all and shrink the public API" beats "stay and rewrite the handlers."

## Validation (post-deploy)

- `go test -mod=vendor ./...` — green across all packages.
- `make build-all` — produces working `hop-agent v0.11.7` + `hop-server v0.11.7`.
- All 41 cargo tests in `clients/desktop/src-tauri/` pass (one tripwire updated to scan `internal/client/local_api.go` instead of `cmd/agent/local_api.go`).
- Dev-deployed to [[../entities/mac-mini]] + [[../entities/mbp]] via `make dev-deploy`. Both system daemons came up cleanly. Mesh handshake completed in ~30s. ICMP through utun: 6-9ms RTT 0% loss mini→laptop. NAT-PMP mappings established. Three watchdogs spawned per-instance. No CRITICAL log lines.
- v0.11.7 deployed to [[../entities/hopssh-cloud]] (Oracle Cloud Linux/arm64) via Nomad. `https://hopssh.com/version` returns `{"current":"v0.11.7","version":"v0.11.7"}`. Lighthouse / control-plane RTT through mesh: 34ms 0% loss.

## Hotfix during rollout (v0.11.6 → v0.11.7)

v0.11.6 had cross-compile failures for Windows on both amd64 and arm64. Root cause: `cmd/agent/wintun_windows.go` was moved to `internal/client/wintun_windows.go` but the sibling `wintun/` directory (containing `wintun/{amd64,arm64}/wintun.dll` referenced via `//go:embed`) was missed in the bulk move. The embed pattern resolves relative to the source file's directory.

Reproduced + fixed locally with one `git mv cmd/agent/wintun → internal/client/wintun`, committed as "Fix: Phase NN — move wintun/ DLL embed dir alongside wintun_windows.go", cut v0.11.7. Linux builds (the prod control plane target) were clean in v0.11.6 too — but deployed v0.11.7 since both Linux and Windows are healthy and version-coherence is better.

**Architectural lesson added to CLAUDE.md:** any `//go:embed` directive's relative-pattern path is part of the file's location contract; bulk-move tooling must move embedded sibling directories alongside the source file.

## What this unblocks

- **Phase NN+1** — `clients/mobile-go/mobilehop/` (gomobile module producing `MobileHop.xcframework` + `mobilehop.aar`).
- **Phase NN+2** — iOS NEPacketTunnelProvider scaffolding inside `clients/desktop/src-tauri/`.
- **Phase NN+3** — Android VpnService scaffolding inside `clients/desktop/src-tauri/gen/android/`.

External prerequisites still apply: NN+2 needs Apple Developer Program ($99/yr); NN+3 needs Google Play Console ($25 one-time). Substrate is no longer the blocker.

## Backlinks

- [[../concepts/client-strategy]] — overall 5-platform delivery strategy (substrate now built per § Status snapshot)
- [[../decisions/client-ios-architecture]] — iOS ADR (status: substrate-built)
- [[../decisions/client-android-architecture]] — Android ADR (status: substrate-built)
- [[../concepts/watchdog]] — three-watchdog architecture preserved through extraction
- [[phase-ledger]] — canonical row at "NN | v0.11.5"
