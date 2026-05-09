---
type: phase
title: Phase ledger — every shipped phase, version, commit
status: current
last_compiled: 2026-05-09
sources:
  - git log
  - CLAUDE.md Discovery Log
---

# Phase ledger

Single source of truth for every shipped phase: letter → version → commit → 1-line summary → status. Built from `git log` 2026-05-09 + cross-referenced with `CLAUDE.md` Discovery Log.

**Naming convention drift**: Phase letters A–E were used TWICE in this codebase — once in April 19 ("multi-network agent" series) and again in April 28–29 ("desktop-client" series). The two batches are disambiguated by date column below. Subsequent phases (G, J, L, N, O, P, Q, R, S, T, U, V, W, X, Y, Z, AA, BB, CC, DD, EE, FF, GG, HH, II, II.2, II.3, II.4, KK) are unique.

## Multi-network agent series (April 19, 2026)

| Phase | Commit | Date | Summary |
|---|---|---|---|
| A | `a189a2f` | 2026-04-19 | Agent: multi-network config registry + legacy migration |
| B | `12446f1` | 2026-04-19 | Agent: fan boot loop out over N mesh instances |
| C | `a5b16f8` | 2026-04-19 | Agent: per-network DNS configuration |
| D | `a5b16f8` | 2026-04-19 | Agent: multi-network CLI — leave, status, restart |
| E | `6c7c45f` | 2026-04-19 | Agent: idempotent `hop-agent install` on Windows |

## Performance series (April 25, 2026)

| Phase | Commit | Date | Summary |
|---|---|---|---|
| B-lite | `0dde59e` | 2026-04-25 | Per-peer EWMA RTT observability |
| G | `76168bc` | 2026-04-25 | HTTPS-distributed self-endpoints close cellular-idle gap |
| G followup | `327b4f9` | 2026-04-25 | Fix screen-share black-out caused by Phase G overlay-IP leak |

## Desktop-client series (April 28 → May 9, 2026)

| Phase | Version | Commit | Date | Summary | Status |
|---|---|---|---|---|---|
| A (desktop) | — | `9e6bb6b` | 2026-04-28 | Auto-convert to system service (Tailscale-style invisible daemon) | Live |
| B (desktop) | — | `7052a44` | 2026-04-28 | Topology dedup, lighthouse, auto-prune zombie nodes | Live |
| C (desktop) | — | `e5da5d7` | 2026-04-28 | Convert regression + login redirect + empty-network UX | Live |
| D (desktop) | — | `c2e566f` | 2026-04-29 | JS endpoint cache invalidation on `agent-ready` | Live |
| J | — | `8f3df49` | 2026-05-01 | install-mac.sh silent failures + system-mode refresh | Live |
| L | — | `e0aab4a`, `1a3cb69` | 2026-04-30 → 2026-05-01 | Cross-platform clipboard sync (server relay + agent opt-in + Settings toggle) | Live (off-by-default) |
| N | — | `28a83e6` | 2026-05-01 | "Hide hopssh from the Dock" toggle (menubar-only mode) | **Live** — superseded by BB's "Show in Dock" affirmative rename (same field, inverted UI) |
| O | — | `a4d2018` | 2026-05-01 | Jargon rewrite for novice users (slice 1) | Live, extended by BB |
| P | v0.10.79 | `a94baf7` | 2026-05-02 | Renewal-goroutine silent death + UI honesty fix | Live |
| Q + P4 | v0.10.80 | `701eda0` | 2026-05-02 | Auto-recover + manual escape hatch (Force renew button) | Live (button removed in R) |
| R | v0.10.81 | `0b7b10d` | 2026-05-02 | Remove user-visible "Force renew" button (engineering capability preserved at `POST /local/renew`) | Live |
| S | v0.10.82 | `0c52943` | 2026-05-02 | Renewal loop survives macOS deep-sleep (60s wall-clock ticker) | Live; documented in `docs/wiki/decisions/phase-s-renewal-ticker.md` |
| T | v0.10.83 | `126dc7b` | 2026-05-02 | Nodes table column toggles + RTT replaces dead Handshake | Live |
| T followup | v0.10.84 | `e5c3b20` | 2026-05-02 | Contain page-level horizontal scroll | Live |
| U | — | `f94b5a6` | 2026-05-03 | Reconcile docs/wiki/ against Karpathy's actual gist | Docs only |
| V | v0.10.85 | `e4efa04` | 2026-05-04 | Member-role enrollment, orphan-node prevention, watchdog churn | Live |
| W | v0.10.87 | `d70eea0` | 2026-05-04 | Desktop "hopssh isn't running" launch-race false-positive (TCP probe retry) | Live |
| X | v0.10.89 | `01237d4` | 2026-05-05 | Stale `state.endpoint` post-daemon-restart + member-role "network not found" | Live |
| Y | v0.10.90 | `36addd8` | 2026-05-05 | Desktop `.app` autostart on macOS login | Live |
| Z | v0.10.91 | `ee3743f` | 2026-05-05 | System-mode mirror files chown self-heal across boot-before-login | Live |
| AA | v0.10.92 | `d7a130c` | 2026-05-05 | Uninstall removes Phase Y's autostart LaunchAgent | Live |
| BB | v0.10.93 | `fa8fbf4` | 2026-05-05 | UX copy audit (28 strings) + Show-in-Dock affirmative rename | Live; supersedes Phase N's negative phrasing |
| (cross-platform fix) | v0.10.94 | `94bd658` | 2026-05-05 | `syscall.Stat_t` cross-compile fix for Phase Z chown helpers | Live |
| CC | v0.10.95 | `90c8d11` | 2026-05-05 | `/version` endpoint reports `max(current, fetched)` | Live |
| DD | v0.10.96 | `053505a` | 2026-05-07 | watchNetworkChanges goroutine wedge detector + Nebula-call timeouts | Live; documented in `docs/wiki/incidents/2026-05-07-mbp-watcher-wedge.md` |
| EE | v0.10.97 | `9eddc0c` | 2026-05-08 | Desktop client polish bundle (prefs corruption logging, account identity, tooltips, onboarding errors, post-uninstall auto-quit) | Live |
| FF | v0.10.98 | `4bce0cb` | 2026-05-08 | In-app Activity view (events log) | Live |
| GG | v0.10.99 | `c7f4c08` | 2026-05-08 | Diagnostics in Settings → About (View agent logs + Copy diagnostic info) | Live |
| HH | v0.11.0 | `ffb58a6` | 2026-05-08 | DNS records read-only view in Connected | Live (read-only — write needs server auth refactor) |
| II | v0.11.1 | `dfc432f` | 2026-05-08 | SSH to peer via Terminal.app (osascript stopgap) | **Superseded by II.3** — `open_ssh_to_peer` removed in v0.11.3 |
| II.2 | v0.11.2 | `1667143` | 2026-05-08 | Peer row OS icons + lighthouse special-case (initial inline-SVG rendering) | Live (SVG icons reverted in II.3, restored with tooltips in II.4) |
| II.3 | v0.11.3 | `ed81147` | 2026-05-08 | In-app Terminal via dashboard webview + dashboard UX consistency (text-label OS, networkId data-flow, `open_terminal_webview` Tauri command) | Live |
| II.4 | v0.11.4 | `1de32ef` | 2026-05-09 | OS brand-mark icons with tooltips on desktop + dashboard (shadcn Tooltip in dashboard, native HTML `title=` in desktop) | Live |
| KK | v0.11.0+ | `866fea4` | 2026-05-09 | Adopt Karpathy behavioral guidelines (CLAUDE.md + skill) | Live |
| LL | v0.11.4+ | `564165f`, `98457da`, `ca61d21` | 2026-05-09 | Pre-release docs audit (3 tiers: phase-ledger, log backfill, README+competitive+architecture+checklist) | Docs only |
| MM | v0.11.4+ | `4473db5` | 2026-05-09 | Architecture body refresh + CHANGELOG + notarization runbook + ADR back-propagation | Docs only |
| **NN** | **v0.11.5** | (this release) | **2026-05-09** | **`internal/client/` substrate extraction** — 22k LOC moved from `cmd/agent/` (`package main`) to `internal/client/` package. FFI-clean public API surface (`Client`, `Config`, `EnrollOptions`, `Snapshot`, etc.). Unblocks iOS + Android via gomobile (NN+1 → NN+3). Documented in `docs/wiki/decisions/client-{ios,android}-architecture.md` (status: substrate-built). | **Live** |

## Status legend

- **Live** — shipped, in production, no known regressions.
- **Superseded by X** — original implementation replaced by a later phase. Original entry kept for historical record.
- **Live (off-by-default)** — feature exists but requires user opt-in.
- **Docs only** — no code change; documentation-only ship.

## Backlinks

- [[../incidents/2026-05-02-mbp-watchdog-deploy-bounce]] — Phase P + v0.10.36 watchdog motivating incident.
- [[../incidents/2026-05-07-mbp-watcher-wedge]] — Phase DD motivating incident.
- [[../decisions/phase-s-renewal-ticker]] — Phase S decision record (only ADR-style phase write-up so far).
- [[../concepts/desktop-client]] — current shipped desktop-client capability inventory.
- [[../concepts/watchdog]] — three-watchdog architecture (Phase P + v0.10.36 + Phase DD).
- [[phase-s]], [[phase-t]], [[phase-nn]] — three per-phase post-mortem pages exist; the rest live in CLAUDE.md Discovery Log entries.
