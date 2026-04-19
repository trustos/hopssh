# Multi-Network Per Agent — Gap Analysis (post v0.10 ship)

*Recorded 2026-04-19 after shipping roadmap #29. Two parallel code-
review passes (concurrency/lifecycle + edge cases/platform) plus a
direct source check. This doc captures what's solid, what's a real
bug to fix soon, and what's a latent risk to log + watch.*

*Status: **v0.10.0** shipped the feature, **v0.10.1** fixed the
critical watcher-lifecycle bugs (G1 + G2) plus three quick-win
latents (L4, L5, L8), **v0.10.2** shipped the two remaining
meaningful recovery paths (L1 partial-migration + L2 registry
backup). The low-probability latents (L3, L6, L7, L9, L10) remain
as documented backlog.*

---

## Verdict

The happy path is solid — four hosts × two networks × sub-25 ms
cross-mesh pings verified in Phase E. The refactor preserved every
server-contract invariant (no schema / API change) and survived
three real bugs found during live testing. Three follow-up passes
(v0.10.1, v0.10.2) closed the lifecycle and recovery gaps.

---

## Definite bugs — FIXED

### G1 — `watchNetworkChanges` goroutine leak ✅ fixed v0.10.1 (`a28519e`)

**Severity:** High. **Files:** `cmd/agent/instance.go:34`,
`cmd/agent/nebula.go:150–`, `cmd/agent/main.go:288`,
`cmd/agent/renew.go:668`.

`meshInstance.cancel` is declared but **never assigned**. `inst.close()`
calls `cancel()` guarded by nil-check so closure looks clean — but
in reality the cancel function is always nil, so cancellation is a
no-op. The network watcher's inner loop is
`for range ticker.C { … }` — no `<-ctx.Done()` branch — so it runs
forever against whatever `*nebula.Control` it captured at boot.

Triggers:
- `hop-agent leave --network X` → registry shrinks, instance's svc
  closes, but the watcher keeps running against the dead Control.
- Cert renewal → `reloadNebula` swaps `inst.svc`, but the watcher
  still holds the pre-swap ctrl (see G2).

Result: steady goroutine accumulation, cycle-per-leave. The calls
themselves (`ctrl.RebindUDPServer`, `ctrl.CloseAllTunnels`) on a
stopped Nebula Control are Nebula-defined no-ops today, but that's
undocumented — not something we should rely on.

**Fix:** give `watchNetworkChanges` a `context.Context`, select on
`ctx.Done()` alongside the ticker, and have `startMeshInstance` +
`reloadNebula` derive an instance-scoped context and assign it to
`inst.cancel`.

### G2 — Stale `*nebula.Control` after cert-renewal reload ✅ fixed v0.10.1 (`a28519e`)

**Severity:** High. **Files:** `cmd/agent/renew.go:629–705`,
`cmd/agent/main.go:288`.

`startMeshInstance` calls `go watchNetworkChanges(inst, ctrl)` once
at boot with the initial ctrl. `reloadNebula`'s cold-start branch
(`inst.svc == nil`) correctly starts a **new** watcher with the new
ctrl (renew.go:668). But `reloadNebula`'s **hot-restart branch**
(svc already running, cert just renewed) calls `oldSvc.Close()` and
replaces `inst.svc` **without restarting the watcher**. The
original goroutine keeps calling methods on the now-dead ctrl.

**Fix:** in the hot-restart path, after `inst.setSvc(newSvc)`,
cancel the old watcher's context and spawn a fresh
`watchNetworkChanges` with the new ctrl. Pairs with G1.

---

## Latent risks (log + revisit)

### L1 — Partial-migration re-run lands in an empty registry ✅ fixed v0.10.2 (`fe12448`)

If `migrateLegacyLayout` fails mid-loop (disk full after `ca.crt`
moved but before `node.key`), the layout is half-flat, half-subdir.
On next boot, the required-files stat at the top of
`migrateLegacyLayout` checks `<configDir>/node.crt` — which already
moved into the subdir — so the migration is skipped. The registry
load then returns an empty `enrollments.json`, the boot loop finds
zero enrollments, and the agent drops to the un-enrolled OS-stack
debug path.

**Severity:** Medium. Recovery requires manual `rm -rf` of the half-
moved subdir + re-enroll.

**Mitigation idea:** detect half-migrated state (subdir exists but
no `enrollments.json`) and either roll back or complete on retry.

### L2 — Corrupt `enrollments.json` has no automatic recovery ✅ fixed v0.10.2 (`fe12448`)

`loadEnrollmentRegistry` returns error on malformed JSON; every
caller `log.Fatalf`s. No `.backup` is maintained. If the file is
truncated (FS crash mid-write — unlikely with `atomicWrite` temp +
rename, but still possible on an NFS-like mount), the agent can't
boot any enrollment.

**Severity:** Medium (vanishingly rare in practice because of
temp+rename atomicity on local FSes).

**Mitigation idea:** keep a rotating `.backup` written after every
successful `saveLocked`; fall back on load if main fails validation.

### L3 — Linux per-link DNS registered on pre-rename interface

`ensureP2PConfig` renames the kernel TUN from `nebula1` →
`hop-<name>` on first boot post-upgrade. But `configureDNS` runs
once at boot against whatever the active interface is at that
moment. If the yaml-rewrite happens mid-startup (before Nebula
starts) the rename is correctly applied before the interface is
created, so this path is mostly fine. The concern: a cert renewal
that triggers a reload AND coincides with a dev name change would
leave the old interface's per-link DNS stale.

**Severity:** Low. We verified the end state on Phase E Linux VM —
both `hop-home` and `hop-work` had correct DNS. But worth re-
checking after a cold reload that touches the dev name.

**Mitigation idea:** when `ensureP2PConfig` rewrites the dev name,
trigger an instance-scoped DNS reconfigure after Nebula restart.

### L4 — Linux drop-in SIGKILL corruption ✅ fixed v0.10.1 (`a28519e`)

`updateResolvedDropIn` uses plain `os.WriteFile` (not
`atomicWrite`). A SIGKILL at the wrong instant truncates the file;
systemd-resolved may then silently drop the whole `[Resolve]` block
on next reload, breaking DNS for **all** merged entries. Recovery
is a re-run of `configureDNS` on next service restart.

**Severity:** Medium. Fix is a one-liner: swap
`os.WriteFile(…, …, 0644)` for `atomicWrite(…, …, 0644)` which
already exists in `renew.go`.

### L5 — HTTP server shutdown uses one timeout across all instances ✅ fixed v0.10.1 (`a28519e`)

`serverSet.shutdownAll` creates a single 5 s context and calls
`srv.Shutdown(ctx)` for each server in sequence. If instance 0 has
a lingering client connection, it burns the full budget; instances
1, 2 get whatever's left over (often nothing). Per-instance timeout
is cleaner.

**Severity:** Low (rare in practice; shutdown is already "best
effort"). Fix is trivial.

### L6 — `--force --name` race against auto-restart

`handleForce` stops the service, removes the subdir, removes from
the registry, then `runAgentInstall` restarts the service. systemd
with `Restart=always` (and Windows SCM's auto-recovery) might
auto-respawn between stop and reinstall, read the post-force
registry (which has the enrollment deleted), and briefly run with
the old state gone before the new state is written. Tight but
nonzero window.

**Severity:** Low for systemd (agent idempotently re-reads the
registry on boot and handles empty-registry gracefully). Worth
verifying on Windows where SCM recovery timing differs.

**Mitigation idea:** hold an agent-wide file lock during enroll /
leave flows so the daemon can't race through it.

### L7 — Duplicate-network check consumes the token before rejecting

`existingEnrollmentForNetwork` fires **after** `POST /api/enroll`
returns — meaning the server already issued a cert and marked the
token consumed. The agent bails before writing anything locally,
but the server has an orphan node that will never heartbeat. The
error message explicitly tells the operator to clean up the
dashboard entry; still a papercut.

**Severity:** Low / UX.

**Mitigation idea (server-side, out of v0.10 scope):** add a
`GET /api/networks/<ca-fingerprint>` lookup so the agent can
pre-check before consuming a token. Out of scope for v0.10 because
it requires a new server endpoint.

### L8 — Windows reserved filenames as enrollment names ✅ fixed v0.10.1 (`a28519e`)

`validateEnrollmentName` only rejects `enrollments.json`. A user
who edits the file by hand to add `{"name": "con"}` (or `prn`,
`aux`, `nul`, `com1`..`lpt9`) would create a directory Windows
interprets as a device file, silently breaking the enrollment.

**Severity:** Low (requires manual JSON edit; CLI path would never
pick these names as defaults). Fix: enumerate Windows reserved
names in `reservedEnrollmentNames`.

### L9 — Case-insensitive FS collision on manual edits

APFS / NTFS treat `work` and `Work` as the same directory. The
validate regex rejects uppercase, so the CLI path is safe. But a
manual `enrollments.json` edit that adds `{"name": "Work"}` passes
`reg.Add` (string equality) and produces two registry entries
pointing at the same physical subdir on disk.

**Severity:** Low (manual edit required). Fix: case-insensitive
compare in `reg.Add` on case-insensitive platforms, or extend the
validate regex to be even stricter.

### L10 — Bundle tarball extraction trusts the archive

`tar xzf <bundle> -C /` blindly extracts to the filesystem root. A
malicious bundle could overwrite `/etc/shadow` via `../../` paths.
Today bundles are only generated by the control plane we trust, so
the attack surface is nil in practice — but defence-in-depth would
extract to a temp dir, validate every entry stays under
`<configDir>`, then move.

**Severity:** Low (trust-boundary is the control plane).

---

## Explicitly verified non-issues

- **`atomicWrite` for `enrollments.json`:** uses temp + rename
  inside the same directory — atomic on all POSIX filesystems and
  on NTFS for local drives. The `Add` / `Remove` roll back in-
  memory state if `saveLocked` errors, so in-memory and on-disk
  can't diverge on a clean write-failure path.
- **Mesh-cross isolation:** each network has its own Nebula CA,
  which is the cryptographic fence. A node in network A literally
  cannot trust a cert from network B, even if both nodes run on
  the same host. Verified by the Phase E evidence — pings only
  succeed between hosts that share *both* networks.
- **Go-metrics name collision with N Nebula instances:** counters
  aggregate across instances (same global registry). Not a crash,
  not a data-corruption issue, just aggregated stats. Log tagging
  via logrus fields disambiguates at the log level.
- **Windows DNS proxy loopback leak on bind failure:** the
  `activeDNSProxies[name] = p` write is *after* the listen-success
  `select`, so a bind failure returns with no map entry. No leak.
- **`runHeartbeat` / `runCertRenewal` goroutines:** both select on
  `ctx.Done()` in their outer loops and exit cleanly when the
  agent-wide context cancels. Only `watchNetworkChanges` is broken
  (G1).
- **Cross-process concurrent enroll + leave:** rare in practice
  (both are CLI commands a human runs serially), and each holds
  the `enrollmentRegistry.mu` around the save-to-disk path. Tighter
  with a file lock, but the in-process mutex plus atomic rename is
  already a big step up from the pre-v0.10 "blow away configDir"
  force flow.
- **Server-side contracts:** no changes needed. Heartbeat,
  renewal, enroll, proxy, events, peer_state all accept per-nodeID
  requests independently and carry no agent-identity assumption.

---

## Test coverage gaps

Addressed in v0.10.1 / v0.10.2:
- ✅ Partial migration failure → boot → recovery (6 new scenarios
  in `legacy_migrate_test.go`).
- ✅ Corrupt `enrollments.json` → boot → fallback to backup
  (4 new scenarios in `enrollments_test.go`).

Still open (add when touching the surrounding code):
- `--force --name` with a supervisor auto-restart racing the
  enroll.
- Concurrent two-instance leave during ongoing proxy connection.
- Windows NRPT rule accumulation across upgrade + leave.
- Linux per-link DNS survival after the first-boot `nebula1` →
  `hop-<name>` rename.

---

## Release history

**v0.10.0** (`a189a2f..5551cd7`) — the feature itself. Phases A–E
plus the three bug fixes caught during live multi-platform
validation (ec7371b, 6e793d9, 6c7c45f) and the dup-network reject
(840c455).

**v0.10.1** (`a28519e`) — watcher lifecycle (G1, G2) + three
quick-win latents (L4 drop-in atomicWrite, L5 per-instance HTTP
shutdown timeout, L8 Windows reserved filenames).

**v0.10.2** (`fe12448`) — recovery paths (L1 half-migration
detect-and-complete, L2 `enrollments.json.bak` backup +
fallback load).

**Remaining backlog (no fix planned):**
- **L3** Linux per-link DNS on pre-rename interface. Verified
  benign in Phase E; revisit if a user reports DNS drift after
  a kernel-TUN enrollment upgrade.
- **L6** `--force` + systemd `Restart=always` race. Theoretical
  window; mitigate with an agent-wide file lock if a real incident
  surfaces.
- **L7** Dup-network check fires after the token is consumed.
  Needs a new `GET /api/networks/<ca-fingerprint>` server
  endpoint to fix properly. Defer until the pre-v0.11 feature
  cycle.
- **L9** Case-insensitive FS collision on manual `enrollments.
  json` edits. Defensible only against malicious/hand-edited
  state; not worth the complexity.
- **L10** Bundle tarball path validation. Trust boundary is our
  own control plane; defence-in-depth only.

**Watch in prod:**
- Agent goroutine count on long-running hosts (`.../debug/pprof/
  goroutine`). If it stabilizes, G1/G2 fixes are holding; if it
  grows monotonically, something else is leaking.
- Dashboard per-node heartbeat age after cert renewals. If ages
  drift up on one instance of a dual-network agent, reloadNebula
  isn't keeping all instances' heartbeats warm.
