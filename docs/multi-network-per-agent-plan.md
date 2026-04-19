# Multi-Network Per Agent — Planning Brief

*Created: 2026-04-19. Status: planning seed for a fresh session.*

This document is the entry point for planning roadmap item **#29 —
Multi-network per agent**. It captures the problem, the known
invariants, the singleton assumptions in the current agent, and the
decisions a planner has to make before writing code. Hand this to the
next session's planner along with the prompt at the bottom.

## Problem statement

Today, one running `hop-agent` process = one mesh membership. If a
user wants their MacBook in "home" AND "work" networks, they must run
two separate agent processes with separate config directories, service
definitions, and bearer tokens. ZeroTier users do this natively (one
daemon joins N network IDs); NetBird supports it; Tailscale has
"Connect" profiles that switch (not simultaneous). This is table
stakes for power users and a gate for team-of-teams workflows.

**Goal:** a single agent process can be enrolled into N networks
simultaneously. Each membership has its own Nebula instance, its own
cert, its own heartbeat cadence, its own DNS scope. User-visible
commands operate per-network (`hop-agent status --network home`) with
sensible defaults (single-network stays ergonomic).

## Why the current architecture can't do this

- `cmd/agent/nebula.go:46` — `currentNebula meshService` is a global
  singleton protected by `nebulaMu`. Every path (`renew`,
  `watchNetworkChanges`, heartbeat, peer-state collection, DNS
  configuration) reads this one variable.
- `cmd/agent/main.go:217-265` — the boot path writes to `currentNebula`
  in three places (cold start, kernel-TUN launch, userspace launch).
- Config dir is a single directory (`~/.hop-agent` or `/etc/hop-agent`)
  holding one cert, one key, one token, one Nebula yaml. File paths
  are hardcoded across agent code.
- `cmd/agent/renew.go:runHeartbeat` is a single goroutine
  (`go runHeartbeat(...)` fired once per `runServe`) that does one
  POST to one endpoint with one bearer token.
- `cmd/agent/nebula.go:watchNetworkChanges` is one polling loop that
  calls `RebindUDPServer()` on the single Nebula instance.
- Service definition (launchd, systemd, Windows SCM) registers one
  agent with one `ExecStart`. No concept of "agent with memberships."

## Invariants already pinned (so we don't paint ourselves into a corner)

During the v0.9.13/v0.9.14 work we deliberately kept the following
decisions compatible with a future #29:

1. **One heartbeat POST = one nodeID = one network.** The server's
   `Heartbeat` handler (`internal/api/renew.go`) decodes a single
   `nodeId` body and never batches across networks. The multi-network
   agent must fire N heartbeat goroutines (one per membership), each
   POSTing independently. No server-side change is needed for that.

2. **`nodes.peer_state` blob has no `network_id` in it.** Peer state
   is a per-node attribute and the nodes row is already network-scoped
   via FK. Don't add network_id into the blob or elsewhere — a node
   belongs to exactly one network; a multi-network agent just has
   N nodeIDs (one per network).

3. **`collectPeerState(ctrl)` takes an explicit `*nebula.Control`**,
   not a global. When the agent runs N Nebulas, the caller iterates
   and passes each.

4. **`signalHeartbeat()` is generic** (no nodeID or network baked in).
   In the multi-network world it becomes per-goroutine local, and
   each goroutine wakes on its own channel.

5. **Activity log events are per-network.** `network_events` is keyed
   by `network_id`. No cross-network event aggregation needed on the
   server side for #29.

6. **`RecordProxyActivity(nodeID)` is already nodeID-addressed**, so
   proxy traffic → heartbeat path works unchanged.

## Decisions the planner has to make (before writing code)

These are the forks where "obvious" choices have big downstream
consequences. Get user alignment in Phase 1/3 of plan mode.

### D1. Agent identity model

- **Option A — One agent, N enrollments** (NetBird-style). The binary
  has a list of enrollments; each is a complete config bundle (cert,
  key, token, Nebula yaml). One process, N Nebula instances.
- **Option B — One agent, one enrollment, multiple processes**. Each
  "network" is a separate agent process with its own config dir and
  service definition. Simpler refactor, less elegant UX.
- **Option C — Hybrid.** One process with N "profiles" that are
  switchable (not simultaneous). This is Tailscale's model. Probably
  not what the user wants (stated: "same as ZeroTier = simultaneous").

**Recommended default:** A. It's the "one daemon, many overlays"
experience the user described.

### D2. Config layout

- **Option A.1 — Subdirs per network.** `~/.hop-agent/<networkID>/`
  each contains `node.crt`, `node.key`, `token`, `nebula.yaml`,
  `peer_state`, etc. A top-level `enrollments.json` lists active
  networks. This mirrors Docker / Kubernetes-style per-context dirs.
- **Option A.2 — Single SQLite**. Agent keeps a local `.db` with one
  row per enrollment. More structured but moves us to "agent has a
  database" which is new surface area.

Recommended default: **A.1** (filesystem-native, debuggable via `ls`,
no new dependency). Cert/key decryption path already exists — reuse
per-dir.

### D3. TUN interface count

- **Option A — One utun per network.** Each Nebula instance gets its
  own TUN device. Fully isolated, matches ZeroTier. Problem: macOS
  kernel TUN needs admin on every `utun` creation; Windows WinTun is
  per-adapter; Linux works fine. Also adds listener goroutines per
  network.
- **Option B — Userspace TUN for additional networks.** Primary
  network gets kernel TUN, extras fall back to userspace. Simpler
  perf story only for one net.
- **Option C — Shared TUN with subnet separation.** Single utun, route
  packets to the right Nebula by destination subnet. Too invasive;
  probably breaks Nebula's assumptions.

Recommended default: **A**, with per-OS validation in the plan.
Each mesh gets its own `utun` / `wintun` adapter. This is what
ZeroTier and Tailscale Connect both do.

### D4. CLI surface

Proposed:

```
hop-agent enroll --endpoint https://hopssh.com         # first-time
hop-agent enroll --endpoint https://other.hopssh.com   # joins second

hop-agent status                                        # lists all
hop-agent status --network home                         # specific
hop-agent leave --network home                          # remove
hop-agent restart --network home                        # per-net
hop-agent restart                                       # all
```

Default behavior with no `--network` and a single enrollment: unchanged
(ergonomic). With multiple enrollments: status lists all, most
mutating commands require `--network` or prompt interactively.

### D5. Service integration

A single LaunchDaemon / systemd unit / Windows service still runs the
process. The process internally manages N memberships. Service files
don't change. `hop-agent install` behaves as today. Enrollment reuses
existing service if already installed; just adds a config subdir.

### D6. Self-update

Self-update applies to the agent binary, not per-network. When the
binary updates, it restarts and picks up all enrollments. No change
to self-update semantics.

### D7. DNS scoping

Each network has its own domain (`*.home`, `*.work`). Agent's DNS
configuration (`dns_darwin.go`, `dns_linux.go`,
`dns_windows.go`/`dnsproxy_windows.go`) must register per-network
split-DNS. Today it writes one `/etc/resolver/<domain>` on macOS or
one NRPT rule on Windows. Needs to loop over all memberships.

### D8. Firewall / listen ports

Each Nebula instance needs its own UDP listen port (default 4242
won't work for all). Strategy: auto-allocate 4242, 4243, 4244, …
per enrollment. Record in the enrollment config. Document firewall
implications in docs/enrollment.md.

## Phased implementation sketch

**Phase A (1 week) — Config model + enrollment.** Refactor
`cmd/agent/config*.go` to a list-of-enrollments model with per-network
subdirs. `hop-agent enroll` adds to the list, doesn't overwrite.
Backward-compat: single-network config-dir layouts auto-migrate.

**Phase B (1-2 weeks) — Runtime: N Nebula instances.** Replace
`currentNebula` global with `map[networkID]*meshInstance` protected by
a mutex. `runServe` loops over enrollments and starts one per network.
Per-network heartbeat goroutine. Per-network
`watchNetworkChanges`. Each instance has its own `utun`.

**Phase C (0.5 week) — CLI + status.** Update commands to accept
`--network` / list multiple.

**Phase D (0.5 week) — DNS + port allocation.** Per-network DNS
configuration. UDP port auto-allocation.

**Phase E (1 week) — End-to-end testing.** macOS + Linux + Windows +
Docker container. Verify a single agent in two networks can
simultaneously ping peers in both, with correct DNS scoping.

**Phase F (0.5 week) — Dashboard.** Server-side: trivial (no schema
change). Frontend: maybe a "Joined networks" view per user. Out of
scope for v1 — the feature is primarily agent-side.

## Risks

1. **Per-OS TUN/WinTun/launchd nuance.** Each OS has its own quirks
   with creating multiple tunnel interfaces as a non-interactive
   service. Needs per-OS fingerprint work before committing.
2. **Nebula internals assume singleton-ish patterns** in some paths
   we haven't fully audited. `nebula.Main(...)` is called once today
   — multiple concurrent instances in one process need vetting. Check
   for globals in `vendor/github.com/slackhq/nebula/*.go` (firewall
   tables, listeners, hostmaps). Expected to be fine since Nebula's
   design is instance-per-config, but worth a read.
3. **Config migration.** Existing single-network agents upgrading to
   the multi-network binary need a lossless auto-migration of their
   config dir. Plan-before-ship: exact migration script.
4. **Service management complexity.** "Add a second network" means
   adding a subdir + re-enrolling. No `systemctl enable` involved.
   But: if the user removes a network, we need to clean up the subdir
   without nuking others. `hop-agent leave --network X` is non-trivial
   to get right.
5. **Self-update triggering global restart.** All memberships drop and
   reconnect together. Fine in principle; document the behavior.

## Out of scope for v1

- **Multi-account** (same user, multiple hopssh control planes). That
  might fall out for free if D1=A with per-enrollment endpoint, but
  don't bake it in as a requirement.
- **Cross-network routing** (packet from one mesh to another). Keep
  mesh isolation; don't route between them inside the agent.
- **Dashboard cross-network views** ("show me all my nodes across all
  my networks"). Separate roadmap item.
- **Per-enrollment capability overrides.** Agent capabilities stay
  agent-wide.

## Prompt for the new session

When starting a fresh session for this work, paste the following as
the opening message:

> Read `docs/multi-network-per-agent-plan.md` — that's the seed
> briefing for roadmap item #29 (multi-network per agent).
>
> I want to move it to the next major effort. Please use plan mode
> (skills don't matter; the workflow does) to turn that seed into a
> real execution plan. Specifically:
>
> 1. Read the code pointers called out in the brief (especially
>    `cmd/agent/nebula.go`, `cmd/agent/main.go`, `cmd/agent/renew.go`,
>    config loading, DNS platform files, service files).
> 2. Audit the vendored `nebula` package for any singleton
>    assumptions we'd need to work around when running N instances
>    in one process. Flag any that block Option A from D1.
> 3. Propose concrete choices for D1–D8 with a one-line rationale
>    each. If any choice changes the difficulty significantly, call
>    it out. Don't just echo the defaults; check them against the code.
> 4. Produce a phased plan (A–F from the brief, or your variant)
>    with: files to create, files to modify, order of commits, and
>    a verification protocol (what evidence do we capture per phase).
> 5. Skip competitive analysis, skip pricing, skip anything beyond
>    the technical plan — we have those layers already.
>
> Constraints:
> - Don't change server-side schemas or APIs. Invariants are pinned
>   in the brief (§"Invariants already pinned") and in CLAUDE.md.
> - Don't ship breaking changes for existing single-network agents.
>   Auto-migrate their config dir on first upgrade.
> - Target platforms for v1: macOS, Linux, Windows, Docker (linux).
>   All four need to work.
>
> Existing context:
> - We just shipped v0.9.17 (version visibility in the dashboard).
> - Recent commits earlier established the per-heartbeat = per-nodeID
>   invariant and peer-state model that's forward-compatible with
>   N-per-agent.
>
> Start with plan mode; don't write code yet. I want a plan to
> approve first.

This prompt is self-contained — the new agent doesn't need to read
this session's history. It has all the pointers to find its way.
