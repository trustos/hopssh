# Peer-state (P2P vs Relay) feature evidence

**Date:** 2026-04-18
**Commits:** `374d9b1` (backend + agent) + `701efac` (dashboard)
**Server:** hopssh.com running `v0.9.10-2-g701efac` (image `ghcr.io/trustos/hopssh:dev-701efac`)

## What the feature does

Each agent heartbeat (5-min cadence) now carries two counts:
- `peersDirect` — peers currently reached via direct UDP (P2P, hole-punched)
- `peersRelayed` — peers currently reached via the lighthouse relay (Nebula's `CurrentRelaysToMe` non-empty)

The server persists the latest values on the nodes row (migration 002) and derives a `connectivity` string (`direct` / `mixed` / `relayed` / `idle`) in NodeResponse. The dashboard renders a colored badge next to each online non-lighthouse node's status.

## Evidence

### 01-round-trip.log — protocol-level round-trip

Five heartbeat variants (direct-only, relayed-only, mixed, idle, omitted) sent with the Mac mini's real agent token against prod. All returned HTTP 200. Proves:
- Request schema parses correctly in the server handler
- Authentication still works with the new payload shape
- The existing "peers" warmup response is preserved (non-breaking)
- Omitted fields are accepted — old agents stay compatible

### 02-real-agent-heartbeat.log — live agent sends fields

Temporary `TESTONLY` log statement in `cmd/agent/renew.go:sendHeartbeat` (since reverted). Captured two heartbeats from the running service on the Mac mini:

```
13:34:08 [heartbeat] TESTONLY no ctrl available     ← first heartbeat fires
                                                       before Nebula startup
                                                       completes. Known
                                                       behavior; agent
                                                       still reports
                                                       nodeId+empty fields.
13:38:57 [heartbeat] TESTONLY peers direct=3 relayed=0   ← Nebula now up;
                                                            lighthouse +
                                                            MacBook + Windows
                                                            all reached P2P.
```

The second line proves:
- `collectPeerState()` is being called from the live agent each heartbeat (not just tests)
- `userspaceMeshService.NebulaControl()` returning non-nil works (Mac mini runs kernel-TUN, but the fix was to ensure BOTH modes return their ctrl — same code path)
- The JSON body shipped to the control plane now includes the fields (they're in `reqBody` before marshal — proven by the log fire)
- At this moment, Mac mini's row in the nodes table has `peers_direct=3, peers_relayed=0 → connectivity="direct"`.

### Startup-timing footnote

The very first heartbeat (within 1 second of agent start) fires before Nebula is ready — `nebulaControlLocked()` returns nil and the payload omits peer counts. This is handled cleanly: server-side `COALESCE(sqlc.narg('peers_direct'), peers_direct)` keeps the prior value (or NULL for a brand-new node) instead of overwriting with zeros. By the second heartbeat (~5 min later), Nebula is up and real counts flow.

If we ever want the first heartbeat to carry peer data too, the fix would be to delay the `runHeartbeat` goroutine until after `currentNebula` is set in main.go. Not urgent — 5-minute delay on first state report is acceptable.

## What this didn't verify

- **Dashboard rendering** — I don't have the user's session cookie, so I couldn't fetch `/api/networks/X` from here. User verifies visually by opening hopssh.com and checking the connectivity badge next to each node's status.
- **Mixed/relayed state transitions** — the round-trip test forced those states via POST, but proving the UI flips requires a real relay path (e.g., cellular CGNAT), which is a separate reproduction.
- **Non-Mac OS** — Linux VM was offline at test time; Windows agent updated + service running, but I didn't add debug logging there to capture its heartbeat.

## How to extend this test

For a real state-flip test:
1. Switch the MacBook to iPhone hotspot (CGNAT → forces relay per past evidence).
2. Wait 5 min for two heartbeats (one MacBook, one peer).
3. Dashboard: MacBook row flips to "Relayed" badge; peers viewing the MacBook row see the same.
4. Switch back to LAN → within 5 min returns to "Direct".
