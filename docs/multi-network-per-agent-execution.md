# Multi-Network Per Agent — Execution Plan (Roadmap #29)

*Created 2026-04-19. Target release: v0.10.0.*

Companion to `docs/multi-network-per-agent-plan.md` (the seed brief).
The brief motivates the feature and enumerates decisions D1–D8; this
document picks the decisions, grounds them in code audit findings, and
lays out the phased work with file-level detail.

## Context

Today one `hop-agent` process = one mesh membership. A MacBook that
wants to join "home" AND "work" needs two processes, two config dirs,
two service units. ZeroTier and NetBird do this natively — table
stakes for power users. This plan turns the seed briefing into a
phased refactor: one agent process holding N simultaneous mesh
memberships, each with its own Nebula instance, cert, heartbeat, DNS
scope, and UDP listener.

The server is already compatible — no schema or API changes needed.
The work is agent-side: collapse the single-network singletons into a
per-network registry, give enrollments their own config subdir, and
fan out the per-enrollment goroutines (heartbeat, renewal, network
watcher) over N instances.

Targets for v1: macOS, Linux, Windows, Docker (linux). Single-network
agents auto-migrate to the new layout on first upgrade — no user-
visible break. Version after ship: **v0.10.0** (minor bump — feature,
back-compat, larger than a patch).

## Audit findings (load-bearing for the plan)

### Nebula vendor package: safe for N instances

- `nebula.Main()` is fully instance-scoped — returns a self-contained
  `*Control`, logger is injected, no package-level mutable state.
- Core structs (`Interface`, `HostMap`, `LightHouse`, `Firewall`,
  `HandshakeManager`, `RelayManager`) are heap-allocated, per-instance.
- Our 10 vendor patches are all per-struct, no new singletons
  introduced.
- UDP/TUN binding is per-config: `listen.port=0` → OS-assigned per
  instance; each call to the Darwin/Linux/Windows TUN factory creates
  a fresh interface.
- **Only soft issue:** `go-metrics` uses static counter names (e.g.,
  `handshakes`, `messages.rx.*`). N instances share one counter. Not a
  crash — just means stats aggregate. Acceptable for v1; log tagging
  via per-instance `logrus.WithField("network", name)` disambiguates.

### Server contracts: fully compatible

- `POST /api/heartbeat` takes one `nodeId` per body
  (`internal/api/renew.go:123`). N heartbeats from one IP are fine.
- `POST /api/enroll` / device flow (`internal/api/enroll.go:107`,
  `device.go:41`) have no "already enrolled" gate on machine identity —
  each call issues a fresh nodeID + token + Nebula IP.
- Cert renewal is node-scoped (`renew.go:37`) — each enrollment renews
  independently.
- Proxy endpoints (`internal/api/proxy.go`) route by `nodeID` URL param.
  Control plane dials the right mesh IP automatically.
- `nodes.peer_state` blob has no `network_id` inside it — comment in
  migration `003_node_peer_state.sql` explicitly notes this is roadmap
  #29 safe.
- Activity events (`internal/api/events.go`) are keyed by `networkID`,
  no cross-network bleed.

### Agent-side singletons (what the refactor has to collapse)

| Name | Location | Scope change |
|---|---|---|
| `currentNebula meshService` | `cmd/agent/nebula.go:46` | → `map[string]*meshInstance` |
| `nebulaMu sync.Mutex` | `cmd/agent/nebula.go:47` | → per-instance mutex + registry mutex |
| `heartbeatTrigger chan struct{}` | `cmd/agent/nebula.go:56` | → per-instance channel |
| `onNebulaRestart func(meshService)` | `cmd/agent/nebula.go:69` | → per-instance callback (captures name) |
| `activeDNSConfig *dnsConfig` | `cmd/agent/nebula.go:75` | → per-instance; platform layer manages aggregation |
| `activeDNSProxy *windowsDNSProxy` | `cmd/agent/dnsproxy_windows.go:43` | → `map[networkName]*windowsDNSProxy` on separate loopback IPs |
| `configDir string` | `cmd/agent/enroll.go:29` | → stays (top level); per-network subdirs underneath |
| `enrollTunMode`, `skipService` | `cmd/agent/enroll.go:61,64` | → stays (enrollment-time flags, harmless) |

The `runServe` boot path (`cmd/agent/main.go:94–339`) sets
`currentNebula` in three places (kernel launch, userspace launch,
OS-stack-only fallback) and wires one `runHeartbeat` + one
`runCertRenewal` + one `watchNetworkChanges` goroutine. All three
spots become "for each enrollment, start an instance." The single
`*http.Server` stays (same handler mux), but each instance gets its
own mesh listener on its own Nebula IP — no port collision because
each mesh has a distinct IP range.

### What does NOT need to change

- Service definitions (systemd unit, launchd plist, Windows SCM entry) —
  still one binary, one ExecStart (`hop-agent serve`). The process
  handles N memberships internally.
- Self-update semantics — restart picks up all enrollments.
- `agentAPIPort = 41820` — binds on each mesh IP independently, no
  collision.
- Server code — zero changes.

## D1–D8 decisions (with rationales)

| # | Decision | Choice | Rationale |
|---|---|---|---|
| D1 | Identity model | **A — one process, N enrollments** | Matches ZeroTier daemon UX. Server is already compatible; no reason for B (multi-process) or C (switchable profiles). Per-enrollment endpoint field technically enables multi-control-plane as a free side-effect (not tested for v1). |
| D2 | Config layout | **A.1 — subdirs per network** under `<configDir>/<name>/` with top-level `enrollments.json` index | Filesystem-native, debuggable (`ls`, `cat`), no new SQLite dependency. Each subdir holds the exact file set today (`ca.crt`, `node.crt`, `node.key`, `token`, `endpoint`, `node-id`, `nebula.yaml`, `tun-mode`, `dns-domain`, `dns-server`). Existing atomic-write + encryption paths reuse unchanged. |
| D3 | TUN count | **A — one utun/wintun per network, kernel-TUN path** | Userspace mode always works (gvisor netstack is in-process, isolated). For kernel mode: macOS assigns `utun0`, `utun1`, ... automatically; Linux creates distinct interfaces; Windows creates distinct WinTun adapters. All three already work for single-network-as-root installs — we just don't assume only one exists. |
| D4 | CLI surface | Name-addressed: `--network <name>`. Single-enrollment defaults unchanged. | `hop-agent enroll --endpoint X [--name home]` adds; `hop-agent status` lists all; `hop-agent leave --network home` removes. Require `--network` when multiple exist and the op is mutating (`leave`, `restart`). |
| D5 | Service integration | **Unchanged.** One systemd unit / plist / SCM entry. Process multiplexes. | No service-layer complexity; `hop-agent install` is a one-time op. Second `hop-agent enroll` skips service install when one already exists. |
| D6 | Self-update | **Unchanged.** All enrollments drop + reconnect together on binary swap. | Documented behavior; acceptable because rollover is <5s for typical mesh. |
| D7 | DNS scoping | Per-network on each platform, with platform-specific aggregation. | macOS: one `/etc/resolver/<domain>` file per network (already per-file). Linux per-link: one Nebula iface per network, register each via `resolvectl`. Linux drop-in fallback: single `/etc/systemd/resolved.conf.d/hopssh.conf` regenerated from ALL enrollments (space-separated `DNS=` and `Domains=`). Windows: spawn one forwarder per network on separate loopback IPs (`127.53.0.1`, `127.53.0.2`, ...), one NRPT rule per domain pointing to its loopback. |
| D8 | UDP listen ports | **`listen.port=0` (OS-assigned)** for all enrollments | Nebula supports it, removes all port-collision/pre-allocation complexity. Only observable difference: `netstat` shows random ports instead of `4242,4243,…`. Document in enrollment guide. |

**Difficulty callouts** (choices that change scope significantly):

- **D7 Windows** is the single biggest "surface" change on one
  platform. Need to allocate loopback IPs, track them per network,
  and keep NRPT rules in sync. Estimated ~200 LOC net in
  `dnsproxy_windows.go` + `dns_windows.go`.
- **D7 Linux drop-in** regeneration requires a deterministic merge —
  sorting enrollment names before writing to avoid spurious file
  diffs. Not hard, but must have a test to prevent churn.
- **D4 CLI** — the biggest user-visible change. Must thread
  `--network` through ~8 subcommands and keep single-enrollment
  ergonomic.

## Naming enrollments locally

The user-visible identifier per enrollment is a **short human name**,
not a UUID. Selection at enrollment time, in priority order:

1. `--name <name>` flag (if user specified).
2. `enrollResponse.DNSDomain` if present (e.g., `home`, `prod`, `zero`).
3. Fallback: first 12 hex chars of CA cert SHA-256 fingerprint.

Name must match `[a-z0-9][a-z0-9-]{0,31}`. Collision (already used by
another enrollment on this agent): prompt user for `--name` override.
The directory on disk matches this name exactly.

The **server is never told** this name — it's purely a local disk
label. This keeps the "no server changes" constraint clean.

## Phased plan

### Phase A — Enrollment registry + config migration (~1 week, ~3–4 commits)

**New files:**

- `cmd/agent/enrollments.go` — registry type (load/save
  `enrollments.json`, list/add/remove), `Enrollment` struct (`Name`,
  `NodeID`, `Endpoint`, `TunMode`, `CAFingerprint`, `EnrolledAt`),
  helper `enrollmentDir(name) string`.
- `cmd/agent/legacy_migrate.go` — one-shot legacy migration: if
  `<configDir>/node.crt` exists but `<configDir>/enrollments.json`
  does not, read the files in place, choose a name (DNS domain or CA
  fingerprint), move everything into `<configDir>/<name>/`, write
  `enrollments.json`. (Named to avoid collision with the existing
  `migration.go`, which is the QUIC migration probe CLI.)

**Modified files:**

- `cmd/agent/enroll.go` — `runEnroll` reads registry first; new
  enrollment computes name, writes to `<configDir>/<name>/`, appends
  to registry. `--force` applies only to one `--network`, not the
  whole top-level dir. `installService()` skipped if service exists.
- `cmd/agent/main.go` — `runServe` calls `migrateLegacyLayout()`
  before flag parse completes; registry is loaded once, iterated
  below (Phase B).
- Existing callers of `configDir` to file paths (`token`, `node.crt`,
  `nebula.yaml`, etc.) migrate to `enrollmentDir(name) + "/" + file`.
  Touches most of `cmd/agent/*.go`.

**Commits (order):**

1. Add `enrollments.go` with Registry type + tests. Not wired up yet.
2. Add `legacy_migrate.go` + tests. Migration runs on startup but
   registry isn't read yet.
3. Refactor `enroll.go` to write to subdir + append to registry.
   Single-enrollment flow identical; multi-enroll still blocked at
   runtime (Phase B).
4. Thread `enrollment *Enrollment` argument through file-path callers
   in `nebula.go`, `renew.go`, `dns.go`. Still single-instance.

**Verification:**

- Unit tests: registry round-trip (load after save).
- Migration test: fake legacy layout in tempdir → run
  `migrateLegacyLayout()` → assert subdir exists, `enrollments.json`
  correct, legacy paths gone.
- Smoke: existing single-network agent upgraded to this commit range
  keeps working with no user intervention. Test on Mac mini via
  `make dev-deploy`.

### Phase B — Runtime: N Nebula instances (~1.5 weeks, ~4–5 commits)

**New files:**

- `cmd/agent/instance.go`:

  ```go
  type meshInstance struct {
      enrollment       *Enrollment
      svc              meshService
      svcMu            sync.Mutex
      heartbeatTrigger chan struct{} // buffered 1
      dnsConfig        *dnsConfig
      cancel           context.CancelFunc
      onRestart        func(meshService) // replaces global onNebulaRestart
  }

  type instanceRegistry struct {
      mu     sync.RWMutex
      byName map[string]*meshInstance
  }
  ```

  Methods: `Start(enrollment)`, `Stop(name)`, `StopAll()`, `Get(name)`,
  `ForEach(fn)`, `Control(name) *nebula.Control`.

**Modified files:**

- `cmd/agent/nebula.go` — remove globals `currentNebula`, `nebulaMu`,
  `heartbeatTrigger`, `onNebulaRestart`, `activeDNSConfig`. Helpers
  like `nebulaControlLocked()` become methods on `*meshInstance`.
  `signalHeartbeat(inst)` takes the instance.
- `cmd/agent/renew.go` — `runHeartbeat`, `runCertRenewal` take an
  `*meshInstance`. Cert renewal reads/writes
  `<enrollment dir>/node.crt` (already done in Phase A step 4). The
  heartbeat body still carries one `nodeId` — ≤one POST per instance.
- `cmd/agent/main.go` `runServe` — boot loop:

  ```go
  reg := newInstanceRegistry()
  for _, e := range registry.List() {
      reg.Start(e)  // starts Nebula + heartbeat + renew + watchChanges
  }
  // One http.Server; per-instance mesh listeners via instance.Listen()
  for _, e := range registry.List() {
      inst := reg.Get(e.Name)
      ln, _ := inst.svc.Listen("tcp", fmt.Sprintf(":%d", agentAPIPort))
      go srv.Serve(ln)
  }
  ```

- `cmd/agent/watch.go` (extracted from `nebula.go`) —
  `watchNetworkChanges` takes `*meshInstance`, signals its own
  heartbeat channel.
- Per-instance shutdown: `instanceRegistry.StopAll()` replaces the
  single `currentNebula.Close()` in the shutdown path.

**Commits (order):**

1. Add `instance.go` + `instanceRegistry`. No callers yet. Unit tests
   cover start/stop lifecycle with mock `meshService`.
2. Replace `currentNebula` global in `nebula.go`/`renew.go` with
   registry lookup. Single-enrollment loop still. Verify on Mac mini.
3. Loop `runServe` over `registry.List()`. Single-enrollment case
   produces identical behavior. Multi-enrollment starts two Nebulas
   (but we don't have a way to add a second one yet — that comes in
   Phase E).
4. Multi-enrollment smoke test: manually create a second subdir with
   a fake enrollment, verify the loop starts both instances without
   crashing (fails at lighthouse contact, which is fine for the test).
5. Cleanup: remove all now-unused package globals. Run `go vet` +
   `golangci-lint` to confirm no dead code.

**Verification:**

- Mac mini running v0.10.0-dev single-network must behave identical
  to v0.9.17 (same heartbeat cadence, same DNS, same proxy).
- Synthetic two-instance test: spawn second mesh in a test harness
  (no lighthouse — expected failure state). Assert registry shows
  two instances, no goroutine leak on shutdown.
- Race detector: `go test -race ./cmd/agent/...` clean.

### Phase C — DNS + Windows loopback allocation (~0.5 week, ~2–3 commits)

**Modified files:**

- `cmd/agent/dns.go` — `configureDNS` and `cleanupDNS` take an
  `*meshInstance`. Store the `dnsConfig` on the instance, not a
  global.
- `cmd/agent/dns_darwin.go` — unchanged logic (per-file per-domain
  already multi-safe); just route via instance.
- `cmd/agent/dns_linux.go`:
  - Per-link path: register per Nebula interface per instance —
    already multi-safe.
  - Drop-in fallback path: extract current single-domain writer into
    a `writeDropIn(enrollments []*Enrollment)` helper that merges
    all domains into one `/etc/systemd/resolved.conf.d/hopssh.conf`
    with a single `[Resolve]` section and space-separated `DNS=` +
    `Domains=`. Sort entries by name for deterministic output. Call
    on any add/remove.
- `cmd/agent/dnsproxy_windows.go`:
  - Replace `activeDNSProxy` global with
    `map[networkName]*windowsDNSProxy`.
  - Allocate loopback IP per instance sequentially from
    `127.53.0.1`, `127.53.0.2`, …, `127.53.0.254`. Persist allocation
    in the enrollment record (or recompute from registry order).
  - Each proxy binds its loopback IP on port 53.
- `cmd/agent/dns_windows.go` — NRPT rule per domain, pointing to
  that domain's loopback IP. On cleanup, remove only that domain's
  rule.

**Commits:**

1. Route DNS config through instance. Windows still single-network.
2. Windows: loopback-per-instance proxy + per-domain NRPT rule.
3. Linux drop-in merger + deterministic sort test.

**Verification:**

- macOS: two enrollments; `dig @127.0.0.1 -p <port> <host>.home` and
  `dig @127.0.0.1 -p <port> <host>.work` both resolve via the mesh.
  `/etc/resolver/home` and `/etc/resolver/work` both present.
- Linux per-link: `resolvectl status` shows two Nebula interfaces,
  each with its domain.
- Linux drop-in: force the fallback (non-53 port); inspect
  `/etc/systemd/resolved.conf.d/hopssh.conf` — one `[Resolve]` block
  with both domains.
- Windows: `Get-DnsClientNrptRule` shows two rules, different
  loopbacks. `Resolve-DnsName foo.home` and `Resolve-DnsName bar.work`
  resolve correctly.
- Leave one network → only its DNS cleaned up, other still works.

### Phase D — CLI commands (~0.5 week, ~2 commits)

**Modified files:**

- `cmd/agent/status.go` — `hop-agent status` lists all enrollments
  (or shows one if `--network <name>`). Output includes per-network
  Nebula IP, peer count, heartbeat age, DNS domain.
- `cmd/agent/main.go` — new subcommand dispatch:
  - `leave` → `runLeave(args)`; requires `--network` if >1 enrollment.
  - `restart` → `runRestart(args)`; optional `--network` (all if
    omitted).
- `cmd/agent/enroll.go` — `--name` flag (optional).
- `cmd/agent/leave.go` (new) — remove enrollment: stop instance,
  cleanup DNS, remove subdir, remove from registry. Prompt
  confirmation unless `--yes`.

**Commits:**

1. `status` + `restart` multi-aware.
2. `leave` + `enroll --name` + docs update in `docs/enrollment.md`.

**Verification:**

- `hop-agent enroll --endpoint https://hopssh.com --name home` then
  `hop-agent enroll --endpoint https://hopssh.com --name work` → two
  entries; `hop-agent status` shows both; `hop-agent leave --network work`
  removes work cleanly; `hop-agent status` now shows one.
- `hop-agent leave` (no `--network` flag, 2 enrollments) → error
  telling user to specify.
- `hop-agent leave` (no flag, 1 enrollment) → prompts and leaves.
- `hop-agent restart` → both instances restart;
  `hop-agent restart --network home` → only home.

### Phase E — E2E testing across all 4 platforms (~1 week)

**Test matrix (manual, documented evidence per run):**

| Platform | Kernel TUN | Userspace | Two networks | DNS both | Peer ping both | Leave+add | Self-update |
|---|---|---|---|---|---|---|---|
| macOS 26 (arm64) |  |  |  |  |  |  |  |
| macOS 26 (x86-64 Rosetta) | N/A |  |  |  |  |  |  |
| Ubuntu 24.04 LTS |  |  |  |  |  |  |  |
| Ubuntu 25.10 |  |  |  |  |  |  |  |
| Windows 11 (arm64) |  |  |  |  |  |  |  |
| Docker (linux/arm64) | N/A |  |  |  |  |  |  |

**Per-row scenario:**

1. Fresh install → enroll in `home` (prod hopssh.com).
2. Enroll in `work` (second test network, likely same control plane).
3. `hop-agent status` shows both. Each has distinct Nebula IP.
4. Ping a peer in `home` and a peer in `work` from the same box.
   Both succeed within 2s.
5. Resolve `<peer>.home` and `<peer>.work` — both return mesh IPs.
6. Wait 10 min → both tunnels still up, two heartbeats on dashboard.
7. `hop-agent leave --network work` → clean removal; `home`
   unaffected.
8. Self-update to next patch (fake bump locally) → both come back up.

**Evidence capture:** per-row, collect `hop-agent status`,
`resolvectl status` / `scutil --dns` / `Get-DnsClientNrptRule`,
`netstat -an | grep nebula`, dashboard screenshot.

Store under `spike/multi-network-evidence/<platform>/`.

### Phase F — Docs + v0.10.0 release (~0.5 week)

**Modified files:**

- `docs/features.md` — move item #29 from roadmap to shipped.
- `docs/roadmap.md` — mark complete; dashboard "joined networks"
  view stays on roadmap for v0.11+.
- `docs/enrollment.md` — add "Joining a second network" section.
- `CLAUDE.md` — append a Discovery Log entry summarizing
  load-bearing findings (Nebula N-instance safety, Windows loopback
  scheme, Linux drop-in merger).
- README badge update.

Release:

- `make release BUMP=minor` → v0.10.0.
- Update `oci_nomad_cluster/jobs/hopssh.nomad.hcl` to the new tag.

**Verification:**

- Production agents on Mac mini + laptop auto-update to v0.10.0; each
  keeps its single enrollment (auto-migration); both still healthy on
  dashboard.

## Risks & mitigations

- **Nebula metrics collision.** Shared global counters across N
  instances. Acceptable for v1 (logs tag per network via logrus
  field). If per-network stats become important, revisit with a
  wrapper that supplies a dedicated go-metrics Registry per Main call
  — needs Nebula patch (not in scope).
- **Windows loopback IPs.** `127.53.0.0/24` is non-routable but not
  reserved. Collision with an exotic user setup is theoretically
  possible — probe before binding and bump to next IP if in use.
- **Linux drop-in churn.** If multiple adds happen concurrently, the
  merged file can see partial writes. Use the same atomic-write
  helper already in `renew.go` (temp + rename), plus a registry
  mutex around the write.
- **Migration of an agent with an expired cert.** The single-network
  `reloadNebula` cold-start path (`renew.go:577`) assumes one HTTP
  listener. Migration must preserve this: for a single surviving
  enrollment, the startup path continues to work exactly as today.
  Test: migrate a machine with an expired cert → renewal still kicks
  in → Nebula starts for the migrated subdir.
- **Self-update rollover latency.** All memberships drop together.
  Typical recovery is <5s per mesh — document the behavior; don't
  try to stagger.
- **Service migration on upgrade.** The existing systemd/launchd/SCM
  unit keeps its ExecStart. The binary at `/usr/local/bin/hop-agent`
  is swapped in place. No service re-install needed during the
  v0.9.x → v0.10.0 upgrade. Verified by `hop-agent --version`
  swapping across Mac mini + laptop during dev-deploy.

## Files to touch (summary)

**Create:**

- `cmd/agent/enrollments.go`
- `cmd/agent/legacy_migrate.go`
- `cmd/agent/instance.go`
- `cmd/agent/watch.go` (extracted)
- `cmd/agent/leave.go`

**Heavy modify:**

- `cmd/agent/main.go` (boot loop, runServe)
- `cmd/agent/nebula.go` (strip globals)
- `cmd/agent/renew.go` (heartbeat + renewal take instance)
- `cmd/agent/enroll.go` (subdir write, --name, registry append)
- `cmd/agent/dnsproxy_windows.go` (per-network proxies)
- `cmd/agent/dns_windows.go` (per-network NRPT)
- `cmd/agent/dns_linux.go` (drop-in merger)
- `cmd/agent/status.go`
- `cmd/agent/dns.go` (instance-scoped configureDNS)

**Light modify:**

- `cmd/agent/service.go`, `service_windows.go` (no behavior change,
  but skip install-if-present logic)
- `cmd/agent/shell*.go`, `proxy.go` (file-path helpers if they read
  `configDir` directly — most should go through the enrollment
  argument already threaded in Phase A step 4)
- `docs/features.md`, `docs/roadmap.md`, `docs/enrollment.md`,
  `CLAUDE.md`

**No change:**

- `internal/**` (server-side) — verified compatible.
- `frontend/**` — cross-network dashboard view is explicit v2.
- `vendor/github.com/slackhq/nebula/**` — no new patches needed.
- `patches/**` — existing patches stay.

## Verification protocol (cumulative)

Per phase, before moving to the next:

1. `go vet ./... && go test ./cmd/agent/... -race` clean.
2. `make dev-deploy` to Mac mini + laptop — both upgrade cleanly and
   single-network smoke (ping a peer, shell, file upload, DNS)
   passes.
3. Dashboard shows both agents online, correct version, peers
   reported.
4. No new goroutine leaks under a 30-minute soak (use
   `go tool pprof .../debug/pprof/goroutine`).

End-to-end sign-off criterion (Phase E complete):

- All six rows in the platform matrix green.
- A single agent in two networks survives a 10-min idle + a cert
  renewal + a self-update without human intervention.
- Evidence bundle committed to `spike/multi-network-evidence/`.

Ship as **v0.10.0**.
