---
type: phase
title: Phase T — dashboard table column toggles + RTT replaces dead Handshake
status: shipped
last_compiled: 2026-05-03
ship_version: v0.10.83 + v0.10.84
ship_date: 2026-05-02 → 2026-05-03
sources:
  - frontend/src/routes/(app)/networks/[id]/+page.svelte
  - frontend/src/routes/(app)/+layout.svelte
  - frontend/src/lib/components/app-sidebar.svelte
  - cmd/agent/peerstate.go (LastHandshakeSec dropped)
  - internal/api/types.go (RTTms added)
  - internal/api/peer_state_test.go (regression coverage)
---

# Phase T — dashboard table UX

## TL;DR

Two-version slice. **v0.10.83** added a Tailscale/Linear-style "Columns" dropdown to the Nodes table at `/networks/<id>` and replaced the always-`unknown` Handshake column with **RTT** (populated from `peer.rttMs` which was always being collected agent-side). **v0.10.84** fixed the page-level horizontal scroll: `Sidebar.Inset` is a flex child whose default `min-width: auto` lets descendant content push past the viewport — added `min-w-0` to the chain + `overflow-x-hidden` on the page wrapper. Optional T4 (column-toggle pattern on DNS/Members/Audit tables) deferred.

## Problem

Pre-v0.10.83 dashboard:
- 9 columns hidden via Tailwind `hidden ?:table-cell` breakpoints. At 1280–1440px, all visible; some wrap onto 3 lines (`yavors-/macbook-/pro`).
- `overflow-hidden` container clipped — no fallback scroll for narrow viewports.
- "Handshake" column always read `unknown`. Comment in `cmd/agent/peerstate.go:14` said "Nebula's public API doesn't expose it today" — stub from day one.
- Page-level horizontal scroll dragged the sidebar out of frame on narrow viewports.

## What shipped

### v0.10.83 (Phase T1+T2+T3)

- **Columns dropdown** — `DropdownMenu` + `DropdownMenuCheckboxItem` from shadcn-svelte. Status / Name / Actions pinned; 7 toggleable (Capabilities, IP, DNS, OS, Client, Last Seen, Version). State persisted in `localStorage` (`hopssh.nodes-cols-v1`). Viewport-aware first-visit defaults (narrow → IP/Client/Version off).
- **RTT replaces Handshake** — `cmd/agent/peerstate.go::PeerDetail` had `RTTms` populated since pre-v0.10.83 by `runPathQuality` (EWMA TCP-connect to `<peer>:41820`, smoothed alpha=0.3 every 10s). The server-side mirror in `internal/api/types.go::PeerDetail` was silently dropping it on deserialization. Added the `rttMs` JSON tag; round-trips now.
- **`LastHandshakeSec` dropped** from agent + server + frontend types. Mixed-version fleets stay stable because old agents send the field and `json.Unmarshal` silently ignores it (proven by `TestParsePeerState_OldAgentLastHandshakeIgnored`).
- **Name truncation** — `max-w-[220px] whitespace-nowrap truncate` + `title` tooltip.
- **Hide-offline toggle** — checkbox next to Columns when offline count > 0. Persisted.
- **DNS + Members table containers** — switched `overflow-hidden` → `overflow-x-auto` for graceful degradation.

### v0.10.84 (Phase T followup — page-level scroll fix)

User reported the table's `overflow-x-auto` wasn't working: horizontal scroll still happened at the whole-page level, dragging the sidebar out of frame.

Root cause: shadcn's `SidebarInset` is a flex child with default `min-width: auto`. Wide table content pushes the flex item past its allotted space; nested `overflow-x-auto` can't contain a parent that's already grown.

Fix:
- `frontend/src/routes/(app)/+layout.svelte` — wrap children in `<div class="min-w-0 w-full overflow-x-hidden">`.
- `frontend/src/lib/components/app-sidebar.svelte` — add `min-w-0` to `SidebarInset`, the inner flex-col wrapper, and `<main>`.
- Drop `sticky top-0 z-10 bg-background` from `Table.Header` — once page scroll is contained, sticky inside `overflow-x-auto` interacts oddly and isn't needed.

## Test coverage

`internal/api/peer_state_test.go` — 6 tests:

| Test | Purpose |
|---|---|
| `TestParsePeerState_RTTRoundTrip` | agent JSON with `rttMs` round-trips through `parsePeerState` |
| `TestParsePeerState_RelayedPeerHasZeroRTT` | missing `rttMs` parses to 0 (relayed peers don't include it) |
| `TestParsePeerState_OldAgentLastHandshakeIgnored` | mixed-version fleets stay stable across the field drop |
| `TestParsePeerState_NilOrEmptyReturnsNil` (3 sub-tests) | caller contract preserved |
| `TestPeerDetail_NoLastHandshakeSecField` | source-scan tripwire — JSON output cannot reintroduce the dropped column |

## Optional polish (deferred)

- **T4** — apply the Columns pattern to DNS Records / Members / Audit Log tables. Same primitive, extracted into `frontend/src/lib/columns-state.svelte.ts`. Skip until those tables actually outgrow the breakpoint pattern.

## Postscript: watchdog noise during deploy bounce

Shipping v0.10.84 caused [[../concepts/watchdog]] to fire on [[../entities/mbp]]'s `work` network at 23:40:38 (work lighthouse UDP unreachable for ~7 min during the rolling container swap on [[../entities/hopssh-cloud]]). Functionally correct; just noisy. Optional polish to suppress watchdog when recent heartbeats returned 4xx/5xx is documented as a backlog item, NOT shipped.

## Backlinks

- [[../entities/mbp]], [[../entities/mac-mini]] — peer-state populates from these
- [[../concepts/watchdog]] — interaction during deploy
- [[../entities/hopssh-cloud]] — deploy flow that triggered the watchdog noise
