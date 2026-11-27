# Compile-pass log

Append-only record of wiki operations. One entry per material change. Format mirrors Karpathy's gist convention so Unix tools can filter by date or operation:

```
## [YYYY-MM-DD] <operation> | <short title>

<one-paragraph context>

Pages touched: <tally>
```

Operations: `bootstrap`, `ingest`, `query`, `lint`, `schema` (when SCHEMA.md itself evolves). Filter examples: `grep '^## \[2026-05' log.md` → all May entries. `grep 'lint |' log.md` → all lint passes.

---

## [2026-05-03] bootstrap | Initial seed of docs/wiki/

Seeded SCHEMA.md, index.md, log.md, entity pages (mbp, mac-mini, hopssh-cloud), concept pages (sleep-wake, cert-renewal, watchdog, macos-system-mode), phase pages (phase-s, phase-t), benchmark placeholder (cellular-baseline). Source material drawn from existing `CLAUDE.md` Discovery Log + `docs/sleep-wake-plan.md` + 2026-05-02 forensic SSH probes on `10.42.1.6`.

Pages touched: 13 created, 1 (`CLAUDE.md`) updated with wiki pointer.

## [2026-05-03] schema | Reconcile against Karpathy's actual gist (Phase U)

Initial bootstrap was built from the Medium article alone. Fetched the source gist via WebFetch and found 6 concrete deltas: missing `raw/assets/` carve-out, log-entry format divergence, ingest framed as silent compile rather than conversation, lint missing the web-search-gap-fill clause, no LLM-image-handling note, schema not marked as living. Closed all six in this pass. No content pages changed — only `SCHEMA.md`, this `log.md`, and `index.md`.

Pages touched: 3 updated (SCHEMA.md, log.md, index.md). 0 new, 0 deleted.

## [2026-05-03] schema | Phase V — adopt dev-wiki framework v0.1.0

Generalized the hopssh-only wiki design into the portable [dev-wiki framework](https://github.com/trustos/dev-wiki) (sibling repo at `/Users/tenevi/Projects/Github.Trustos/dev-wiki/`). Ran `init.sh --update-existing` against this wiki: added `decisions/`, `incidents/`, `runbooks/` dirs (3 new page types matched to dev-domain question shapes); added `open-questions.md`; vendored `lint.sh` to `scripts/wiki-lint.sh`; recorded framework version in `.framework-version`. Seeded the new dirs with 4 real pages: 1 ADR (Phase S renewal ticker), 1 incident (2026-05-02 watchdog auto-recovery during v0.10.84 deploy bounce), 2 runbooks (mesh kickstart, system-mode binary refresh). Index extended to list new types. Source insights from the $50M Markdown article (Manus todo.md as attention-shaping; OpenClaw durable+dated split; files-first escalation tiers) absorbed into the framework's PATTERN.md.

Pages touched: 4 created (1 ADR + 1 incident + 2 runbooks), 1 created (open-questions.md), 3 updated (index.md, log.md, SCHEMA.md unchanged in this pass).

## [2026-05-03] schema | Phase V.3 — adopt dev-wiki framework v0.2.0 (context-engineering discipline)

Bumped vendored framework from v0.1.1 → v0.2.0. Five gaps closed: (i) `claude-pointer.snippet.md` rewritten from descriptive to directive — CLAUDE.md now mandates wiki consult before refactor / bug-investigation / entity-config and operationalizes three production patterns from the $50M Markdown article (Manus's todo.md rewrite-loop, OpenClaw's compaction-flush, Manus's save-intermediate-results-to-disk); (ii) SCHEMA.md gained `Sources discipline` section (prior-knowledge marker convention) and `Session handoff — three layers of memory` section (in-flight / unresolved / durable); (iii) `lint.sh` added `--missing-sources` mode flagging pages with empty `sources:` AND no prior-knowledge marker — wired into `--all`. Framework's README documents three enforcement tiers (Tier 1 directive wording shipped; Tier 2 section-trigger rules shipped; Tier 3 settings.json hook deferred). CLAUDE.md addition was 24 lines, well under the 200-line adherence threshold the article warns about. Lint --all clean post-sync (5 modes pass in 0.89s).

Pages touched: 0 created, 3 updated (CLAUDE.md, SCHEMA.md, log.md). Plus framework-vendored: `scripts/wiki-lint.sh` + `.framework-version`.

## [2026-05-04] schema | Phase V.4 — adopt dev-wiki framework v0.3.0 (operating playbook + hooks + reasoning capture)

Bumped vendored framework from v0.2.0 → v0.3.0. Three additions: (i) new `docs/wiki/USAGE.md` — the operating playbook ("what to do today") complementing SCHEMA.md ("conventions + reference") — documents the 4-mode flow (Consult / Work / Ingest / Lint), task-shape decision table, concrete cert-renewal walkthrough, quick-reference card; (ii) Claude Code hooks installed at `.claude/settings.json` — three default hooks (SessionStart / PreCompact / Stop) auto-invoke the operating contract at session boundaries with near-zero steady-state token cost (~150/session, never per-tool-call); merged into hopssh's existing settings.json under empty permissions; (iii) Tier R1 reasoning capture — ADR template gained mandatory `## Decision basis` section; new lint mode `--missing-decision-basis` enforces structurally (wired into `--all`); CLAUDE.md mandate added; `decisions/phase-s-renewal-ticker.md` back-filled with real Decision basis content (5 prior-knowledge claims marked HIGH/MEDIUM/LOW). Lint --all now passes 6 modes in 0.88s. Tier R2 thought-log documented as opt-in in USAGE.md; Tier R3 thinking-block externalization explicitly NOT shipped.

Pages touched: 1 created (`docs/wiki/USAGE.md`), 2 updated (`docs/wiki/decisions/phase-s-renewal-ticker.md`, `docs/wiki/log.md`). Plus framework-vendored: `scripts/wiki-lint.sh` + `.framework-version`. Plus settings.json: dev-wiki hooks merged into existing permissions.

## [2026-05-07] ingest | Phase DD wiki coverage + dev-wiki framework v0.3.0 vendored to git

Two-fold wiki ship. (a) Created `incidents/2026-05-07-mbp-watcher-wedge.md` documenting the `watchNetworkChanges` deadlock that motivated Phase DD (v0.10.96): MBP's home-enrollment watcher went silent at 17:22 after a network-change handler entered the rebind block at 18:00:45 and never logged again for 3.5+ hours. Forensic timeline + root cause (vendor Nebula `RebindUDPServer` / `CloseAllTunnels` deadlock — `defer recover()` doesn't catch deadlocks) + fix breakdown (F1 stamp + watchdog, F2 hard 5s timeouts, F3 UI honesty) + architectural lesson (third class of silent goroutine failure; agent now has THREE independent watchdogs). (b) Rewrote `concepts/watchdog.md` from "Stuck-data-plane watchdog (Phase P2)" to "Agent watchdog architecture (3 independent watchdogs)" — comparison table for renewal / data-plane / watcher; common stamp+threshold+cooldown+restartFn pattern; defense-in-depth hard timeouts on the iteration body. (c) Vendor-committed the dev-wiki framework v0.3.0 to git proper — `.claude/settings.json` hooks, `docs/wiki/USAGE.md`, `decisions/phase-s-renewal-ticker.md`, `runbooks/{mesh-dead-kickstart,refresh-system-mode-binary}.md`, `scripts/wiki-lint.sh`, `raw/` source corpus. Force-added the SKILL.md (matches the existing pattern for `.claude/settings.json` + `CLAUDE.local.md` tracked despite `.gitignore: .claude/`).

Pages touched: 1 created (`incidents/2026-05-07-mbp-watcher-wedge.md`), 1 rewritten (`concepts/watchdog.md`), several updated (`index.md`, `open-questions.md`). Plus dev-wiki framework files newly tracked.

## [2026-05-08] ingest | client plans refresh + relocate (Phase II ADRs)

Refresh + relocate of `docs/clients/*.md` (5 files, ~115KB) into the wiki. Each plan gets proper frontmatter + decision-basis section + a "Lessons from macOS Phase V→DD" section back-propagating production-discovered patterns to the not-yet-implemented platforms (Phase X TCP probe pattern, Phase Y autostart-per-OS mapping, Phase Z boot-before-login race, Phase AA uninstall hygiene, Phase BB forbidden-jargon source-scan, Phase DD three-watchdog primitive). Layout change: `client-apps-plan.md` → `concepts/client-strategy.md`; `client-{macos,ios,android}-plan.md` → `decisions/client-{macos,ios,android}-architecture.md`; `client-desktop-supplement-plan.md` → `decisions/client-windows-linux-architecture.md`. All ADRs gain `Decision basis` sections per the dev-wiki R1 reasoning-capture rule.

Pages touched: 5 relocated (git mv preserves history) + content refreshed. Plus `concepts/desktop-client.md` (NEW) — current-shipped-state inventory for the macOS client.

## [2026-05-09] ingest | Karpathy behavioral guidelines (Phase KK) + Phase LL pre-release audit Tier 1

Two ships. (a) Phase KK adopted [forrestchang/andrej-karpathy-skills](https://github.com/forrestchang/andrej-karpathy-skills) (MIT) into the project as both an always-on rule set in CLAUDE.md (§ Behavioral guidelines, above § Coding Principles) and an explicitly-invocable skill at `.claude/skills/karpathy-guidelines/SKILL.md`. Two layers, two roles: CLAUDE.md sets background discipline for every turn; the skill is for explicit `/karpathy-guidelines` invocation. Marketplace install was deliberately skipped — vendor-copy keeps the adoption project-local + reproducible. (b) Phase LL Tier 1 — pre-release audit. Created `phases/phase-ledger.md` (NEW) — single canonical table mapping every phase letter → version → commit → status (live/superseded). Resolves the A/B/C/D namespace collision (April 19 multi-network agent series vs April 28+ desktop-client series). CLAUDE.md gained one consolidated Discovery Log entry covering the entire Phase EE→II.4 series (8 ships) — captures architectural lessons spanning the series rather than 8 per-phase paragraphs. `docs/features.md` refreshed to v0.11.4 (was 8 days stale at Phase N / v0.10.80) with 16 new entries. `docs/roadmap.md` reconciled — added "Recent Releases" section, struck out "no desktop apps" in "Where we lose" (macOS now ships), Phase 2A item #4 (connection diagnostics) marked partial via Phase GG. Plan file annotated with a "this is session-memory, not the durable ledger" header pointing readers at the wiki ledger as canonical. Phase LL Tier 2 (this entry) + Tier 3 (top-level docs sweep) to follow.

Pages touched: 1 created (`phases/phase-ledger.md`), 4 updated (`CLAUDE.md` Discovery Log + `docs/features.md` + `docs/roadmap.md` + `docs/wiki/index.md`). Plus `.claude/skills/karpathy-guidelines/SKILL.md` (NEW). Plus plan file annotation.


## [2026-05-10] ingest | Linux fresh-install panic resolved (v0.11.19) — Start must run before StartLocalAPI

Investigation closed. SSH'd to the aarch64 Ubuntu VM and captured the actual panic from journalctl: `http: panic serving 127.0.0.1:46684: cannot create context from nil parent` originating from `internal/client/client.go:145` inside `startInstance`'s `context.WithCancel(ctx)` with `ctx == nil`. The local API's auto-connect handler reaches `c.runCtx`, which is set by `Client.Start`. Pre-fix, `cmd/agent/main.go` only called `c.Start` when enrollments existed (`else` branch of the listener-decision if/else); on fresh installs (zero enrollments) it skipped Start, but `client.StartLocalAPI` was always called and exposed connect/enroll endpoints whose handlers needed runCtx. Phase NN regression: pre-extraction the equivalent closure captured the agent-wide `renewCtx`, always live regardless of enrollment count. Two-layer fix in v0.11.19 (commit 10ff0c9): (1) `c.Start(shutdownCtx)` runs UNCONDITIONALLY before `StartLocalAPI` — Start is idempotent and a near-no-op with zero enrollments; (2) defensive nil-runCtx guard in `(*Client).connect` returns "client not started" error instead of panicking. Two regression tripwires shipped: source-scan in `cmd/agent/main_test.go` enforces Start-before-StartLocalAPI ordering; behavioral test in `internal/client/local_api_lifecycle_test.go` exercises the nil-runCtx path. Live-verified on the VM: fresh-install `hop-agent v0.11.19-dev` accepts `/local/status`, `/local/connect?enrollment=does-not-exist`, and `/local/enroll/device-flow/start` without crashing. Architectural lesson: any agent state that exposes a network surface BEFORE its full lifecycle is initialized is a panic-trap; force ordering at every entry point AND keep a defensive guard at the read site.

Pages touched: 0 created, 4 updated (`incidents/2026-05-09-linux-desktop-stuck-onboarding.md` flipped open→resolved with captured-panic + Resolution section; `open-questions.md` moved entry to Resolved; `index.md` updated incident summary; this `log.md`). Plus 4 source files committed (cmd/agent/main.go, cmd/agent/main_test.go, internal/client/client.go, internal/client/local_api_lifecycle_test.go).


## [2026-05-10] ingest | Linux desktop first-user-test surfaces stuck-onboarding + Load-failed (open incident) + v0.11.18 platform-neutral copy

Two strands. (a) **First Linux .deb desktop client user-test (aarch64 Ubuntu VM)** surfaces two symptoms: onboarding stuck at step 2 even after device-flow approval, and manual Connect later returns "Load failed" (JS `TypeError: Failed to fetch`). Disk state confirms enrollment IS persisted; both `hopssh-desktop` + `hop-agent` PIDs alive at user's snapshot; journalctl excerpt has Go stack frames at `internal/client/client.go:87` (the `_, err := try()` line in the 4-attempt connect retry) — most likely a panic but the panic message itself wasn't captured before the diagnostic SSH session ended at precompact. Filed as open incident `incidents/2026-05-09-linux-desktop-stuck-onboarding.md` with hypotheses ranked + next-investigation steps; mirrored in `open-questions.md`. **Action**: SSH back to the VM (creds in `e2e-connections` repo), capture the panic message, root-cause from there. (b) **v0.11.18 platform-neutral copy** — three remaining "Mac"-only strings in the dashboard frontend (`/device` authorize page subtitle, network-detail Desktop badge tooltip, sidebar Get-Desktop tooltip) plus one in `internal/client/client.go:109` (port-conflict error → user-visible toast) flipped to "device" / platform-neutral phrasing. Tripwire test in `parallel_install_test.go` updated to match. Linux + Windows desktop clients are first-class as of Stages 5+6 (v0.11.13+); the dashboard copy needs to keep up. v0.11.18 is dashboard + agent only — the Linux Connect crash root-cause investigation is separate work.

Pages touched: 1 created (`incidents/2026-05-09-linux-desktop-stuck-onboarding.md`), 2 updated (`open-questions.md`, `index.md`). Plus 4 source files committed (3 frontend + 1 client.go + 1 test).


## [2026-05-09] ingest | Phase NN — `internal/client/` substrate extraction (v0.11.5)

Extracted ~22k LOC of client-lifecycle code (89 files) from `cmd/agent/` (which was `package main`) into a new `internal/client/` package. The package exposes a gomobile + Rust-FFI compatible public API surface (`Client`, `Config`, `EnrollOptions`, `Snapshot`, `EnrollmentSummary`, `PeerInfo`, `Event`, `EventCallback`, `SubscriptionID`, `InstanceHTTPHook`) with three reflection tripwires in `internal/client/api_constraints_test.go` enforcing no `chan`/`func`/`interface{}` in exported struct fields. `cmd/agent/` reduced to 9 thin-shell files (main.go runServe constructs `*client.Client`, hands a `BuildHandler` hook for the per-instance HTTP listener). Local-API HTTP server moved into `internal/client/` too — its handler-internal coupling to the (now unexported) registries made the originally-planned in-place rewrite a 3-5× larger refactor than the move-into-package alternative; mobile gomobile bindings dead-code-eliminate the unused HTTP server. Three watchdogs (renewal/keepalive/watcher) preserved with stamp-ordering invariants intact; `restartFn` now closes over `c.connect(name)` instead of the runServe-local `connectFn(name)`. Validation: `go test ./...` green; `make build-all` produces working `hop-agent v0.11.5` + `hop-server v0.11.5`; all 41 cargo tests in `clients/desktop/src-tauri/` pass; dev-deployed to mini + MBP, mesh handshake completes ~30s post-restart, ICMP through utun: 6-9ms RTT 0% loss, NAT-PMP mappings established, no CRITICAL log lines.

This unblocks Phase NN+1 (`clients/mobile-go/mobilehop/` gomobile binding), NN+2 (iOS NEPacketTunnelProvider), NN+3 (Android VpnService) — all three were paper-blocked on the substrate per the iOS + Android ADRs. ADR statuses flipped from `proposed/substrate-blocked` to `substrate-built` with progress-checklist marking the substrate done and the gomobile binding + VPN-extension scaffolding as the remaining open work.

Pages touched: 0 created, 6 updated (`docs/wiki/decisions/client-ios-architecture.md` + `client-android-architecture.md` status + path citations; `docs/wiki/concepts/client-strategy.md` status snapshot + project structure; `docs/wiki/concepts/cert-renewal.md` + `watchdog.md` source path updates; `docs/architecture.md` watchdog section header). Plus `CLAUDE.md` Discovery Log entry, `docs/development.md` project structure section, this `log.md`.

## [2026-11-18] ingest | Phase HH (2nd) — all certs expired on MBP after ~6 months; three-layer root cause + fix (v0.11.27)

> **Wrong in parts, corrected by the 2026-11-23 entry below:** the main cause was missed, the household explanation in (A) is unproven, and nothing was shipped.

On yavors-macbook-pro, both enrollments showed "certificate expired" plus `no live instance` and never recovered. Three root causes:

- (A) the server put `/api/renew` + `/api/heartbeat` on the anonymous 10/min/IP `publicRL`, so heartbeats behind one household NAT starved renewals (production does set `--trusted-proxy`, so buckets are per household, not global);
- (B) `Client.connect` tears down an instance whose Nebula failed to start (an expired cert), which killed the renewal goroutine that would have fixed it (regression since v0.11.6);
- (C) every watchdog `restartFn` had been a no-op since v0.10.36, because `connect` short-circuits on "already up".

The fix:

- a dedicated `agentRL`;
- pre-start renewal (`ensureUsableCert`);
- a per-enrollment wall-clock recovery loop owned by the `Client`;
- `restartInstance` as the real restart.

It is covered by 33 agent tests and 2 router tests. In the same pass: Go 1.25.8 → 1.26.0, dependency bumps (chi, quic-go, x/crypto, x/net, x/sys), CI reads `go-version-file: go.mod`, the Dockerfile moved to golang:1.26, and `npm audit fix` ran. govulncheck is clean.

Pages touched: 1 created (`incidents/2026-11-18-mbp-all-certs-expired.md`), 4 updated (`concepts/cert-renewal.md`, `phases/phase-ledger.md`, `index.md`, `open-questions.md`). Plus the `CLAUDE.md` Discovery Log entry.

## [2026-11-18] ingest | Nebula upstream survey (v1.10.3 → v1.11.2 / v1.12.0)

A background research pass compared our Nebula pin (v1.10.3, 25 patches) with upstream through v1.12.0. The raw survey is at `raw/nebula-upgrade/2026-11-18-upstream-survey.md`; I spot-checked the PR and release facts with `gh api`.

Recommended target: v1.11.2. It brings firewall-bypass fixes, a relay re-establishment fix and hostmap/roam race fixes, and lets us drop patches 01 and 03. Patch 25 (clock-skew) stays ours. About 10–14 patches need real rework.

Also corrected stale CLAUDE.md "Vendor Patch" claims:
- PR #1375 was closed unmerged; the actual fix is #1661 in v1.11.0;
- `scripts/check-nebula-patch.sh` doesn't exist;
- there are 25 patches, not 1.

Pages touched: 1 raw source created, `open-questions.md` updated (upgrade entry + patch-12/Makefile entry), `CLAUDE.md` Vendor Patch section rewritten.

## [2026-11-23] correct | Phase HH (2nd) — main cause found, 2026-11-18 explanations corrected, project evaluated

Ran the released code instead of reasoning about it. The main cause is that agents since v0.10.82 never renew before expiry. Also found and fixed: work on closed instances, a process exit when closing an in-app network, a boot panic on an empty `nebula.yaml`, macOS packets held back or dropped, a netstack leak per close, and four bugs in the 2026-11-18 watchdog change itself. New vendor patches 27–30. Patch 12 turned out to be in no clean build. Nothing is committed, released or deployed.

Pages touched: 2 created (`concepts/macos-udp-send.md`, two reports in `raw/evaluation/`), incident page rewritten, 9 updated (`cert-renewal`, `watchdog`, `mesh-dead-kickstart`, `phase-s`, `phase-s-renewal-ticker`, `index`, `phase-ledger`, `open-questions`, this log), `CLAUDE.md`, `patches/README.md`.

## [2026-11-23] ingest | End-to-end tests with the real server and the real agent

The question was whether the fixes have proper E2E tests, mainly of the API. They did not: the existing ones ran real Nebula against a fake control plane, and 22.7 % of the server-side statements were run by any test. Built `internal/e2etest` (the real `hop-server` as a process), 17 API tests, 7 agent tests (the real agent as a process) and tests for `db`, `mesh` and `selfupdate`. On their first runs they found L, M, N, O, Q, S and T; all fixed. What is wrong and not fixed is pinned with `KnownGap`. 81 planted bugs, all caught. `TestE2E_Upgrade` goes from v0.11.26 to this tree and back.

Corrected on the way: "v0.11.27 recovers the MBP" (not if the machine was silent for more than 7 days, L), the recovery loop gave up for good after a 401 (now asks hourly), and the runbook.

Pages touched: 1 created (`concepts/e2e-tests.md`), 8 updated (incident page, `cert-renewal`, `mesh-dead-kickstart`, `index`, `phase-ledger`, `open-questions`, `development.md`, `features.md`), `CLAUDE.md`.

## [2026-11-23] ingest | The tests under the race detector and on Linux

`-race` found one data race in the agent (U). The first run on Linux (local Fedora VM, arm64) failed in six places, all one bug: a closed network keeps its UDP port until the next packet (V). A new root test for kernel TUN found that the device stays too (W) and that programs started from the web terminal inherit it (X). Vendor patches 31 and 32; CI has a step for the root test. Linux passes since.

Pages touched: 4 updated (incident page, `e2e-tests`, `open-questions`, this log), `CLAUDE.md`, `patches/README.md`.

## [2026-11-24] ingest | Switched-off tripwires, WebSocket and clipboard tests, coverage

Read every skipped test. Four that guarded the watchdogs' wiring had been off since May 2026; `TestE2E_BackgroundWork` (and one source scan) replaces them. 14 wiring bugs planted: without it 11 get through, with it none. The first version missed bug C itself (a watchdog "restart" that only reconnects). Four older tests failed whenever UDP 4242 was taken, since fix O made the port probe real; they use a free range now. New: WebSockets through the real agent, `TestE2E_Clipboard`, the clipboard's rate limits. 16 + 3 bugs planted there, all caught; two new gaps pinned. Coverage of the eight server-side packages: 22.7 % before the E2E tests, 72.9 % now.

Test-only changes; run on macOS, not again on Linux.

Pages touched: 3 updated (`e2e-tests`, `open-questions`, this log), `CLAUDE.md` (rule 14).

## [2026-11-24] ingest | Production checked, release v0.11.27 prepared

Looked at production before the release. No device is left on it: the sweep (L) deleted the last two on 2026-08-07. Automatic deletion is off since today (`HOPSSH_NODE_PRUNE_AGE_HOURS=0`), the database is copied, the volume is a local disk. The log explains the 429s (deleted devices that keep asking) and 8 server crashes in June 2026 (the idle reaper, G), and has networks whose lighthouse did not come back after an idle close (V). New test: `TestReapIdleInstances`; its first version passed without the patch it is meant to guard. Found by running `npm run check`: the dashboard's "Clean up" button called a function that does not exist. All tests on Linux again: pass. The workflows read `go-version-file: go.mod` since 2026-11-18, which means Go 1.26.**0**, without the 34 security fixes of its patch releases; they ask for the newest 1.26 now.

Pages touched: 1 created (`runbooks/release-v0.11.27.md`), 8 updated (incident page, `cert-renewal`, `e2e-tests`, `open-questions`, `phase-ledger`, `hopssh-cloud`, `index`, this log), `CLAUDE.md` (rule 15), `CHANGELOG.md`.

## [2026-11-24] ingest | v0.11.27 released, server deployed

`main` pushed and CI green (first run with Go 1.26, the E2E tests and the kernel-TUN step); database copied again; tag; image and binaries checked; server on v0.11.27 since 07:26 UTC. Corrected on the way: the runbook's push command (the repository has a tag `main`), "all other patches apply cleanly" (GNU patch also refuses hunk 3 of 19), and the log line for pruning (v0.11.27 says nothing while it is off). New open items: proof on devices, the mode of the database file, security headers and a dead link.

Pages touched: `runbooks/release-v0.11.27`, incident page, `macos-udp-send`, `e2e-tests`, `cert-renewal`, `phase-ledger`, `hopssh-cloud`, `open-questions`, `index`, this log, `CLAUDE.md`, `patches/README.md`.

## [2026-11-24] ingest | Network lab: real agents behind simulated routers

`internal/netlab` + `scripts/netlab.sh` run the real server and real root agents in Linux namespaces behind simulated routers. Nine faults of v0.11.27 found and fixed by `path_watch.go`, the `preferred_ranges` key and vendor patches 33 to 36; one more came from our own fix of `preferred_ranges` (a one-way LAN went dead) and was fixed by patch 35; three gaps pinned (UDP between devices, no way without UDP, lost big packets). Corrected: the patch 14/15 entries (15 was never in effect). Final code: the whole lab passed three times (37 tests), 39 of 47 planted bugs noticed, the 8 others explained in the page; a clean vendor tree with all patches is identical to the tested one (patch 12 fails as before).

Pages touched: 1 created (concepts/netlab), 6 updated (index, open-questions, concepts/e2e-tests, log, patches/README.md, CLAUDE.md).

## [2026-11-24] ingest | Path fixes: what the first review of the pull request led to

The path loop stamps, has a time limit and survives a panic, and `runWatcherWatchdog` watches it (it looks twice now, so that a machine that wakes up is not restarted). An old `preferred_ranges` list is moved as it is instead of being replaced by ours. Found on the way: a ticker is microseconds early or late, so every limit of the loop took a second longer in about half the cases; it counts whole ticks now. Tests sharpened: `TestOwnNets`, `TestHomeAndPhone`, NAT-PMP errors in the lab; new: `TestE2E_LighthouseAnswers`, and no new tunnel during a session on a path that works. Corrected: `TestServerRestart`'s number (it was the time after the server is back, not the silence), and the two values of `TestRoaming` explained. 25 more planted bugs, all noticed, two only after their tests were sharpened.

Pages touched: 5 updated (concepts/netlab, concepts/e2e-tests and concepts/macos-udp-send (status), open-questions, this log), patches/README.md, CLAUDE.md.

## [2026-11-24] ingest | Path fixes: a second, independent review, and a race found by one run too many

A race in patch 35, found when `TestOneWayLAN` failed once in an extra run, explained and fixed: the first packet was handed to the machine before the path was changed, so a quick kernel answered into the dead path (`concepts/netlab`, cause 12). New: `TestE2E_MovesBeforeItAnswers`. From the second review (two agents on Opus, independent of the first): the path loop logs its start, its end and that it runs, as every long-lived goroutine must (v0.10.26); its comment says what happens after a panic; the rule of v0.10.28 is marked as superseded where patch 35 replaced it. Everything else they found was known, or checked against the code and not a fault; what is left of it is in `open-questions`.

Pages touched: 3 updated (concepts/netlab, concepts/macos-udp-send (date, backlink), open-questions), this log, patches/README.md, CLAUDE.md.

## [2026-11-25] ingest | Two data races fixed, a renewal test for a long outage, field tools

`go test -race` showed two races in Nebula's own code between sending, receiving and status: the address a peer is sent to (patch 37) and a peer's relay list (patch 38); each has a red/green test in the vendored package. New: `TestE2E_RenewalFailsPastExpiry`, a running agent whose renewals are refused until its certificate is no longer accepted. It showed that the agent then waited up to 30 minutes between tries; `renewNow` now keeps the first wait (1 minute) for as long as there is no valid certificate (a 401 keeps the long wait). An audit of the certificate and path tests lists what has no test yet. `cmd/hop-field` (pull request 3) records real machines; its first 4 hours are in `open-questions`.

Pages touched: 2 updated (open-questions, this log), patches/README.md, CLAUDE.md.

## [2026-11-25] ingest | Before v0.11.28: suite, upgrade test and lab on the state to be released

All packages pass, `TestE2E_Upgrade` from v0.11.27 passes, the lab passes except `TestRouterReboot` once (60.9 s of silence, limit 45 s). Run alone it passed 8 of 8 afterwards, 4 with the branch and 4 with main. The mechanism is in `open-questions`; the limit was not changed.

Pages touched: 1 updated (open-questions), this log.

## [2026-11-26] ingest | Nebula v1.10.3 to v1.11.2 (branch `upgrade/nebula-1.11.2`, not merged, not released)

The 34 patches are 10 files now, one theme each, no file in two of them; 01, 02, 03, 12, 13, 27, 30 and 37 are gone because upstream does the same or they were in no build. `make patch-vendor` stops at a patch that does not apply. Nebula logs with `log/slog`; `nebulacfg.Logger` sets the level, which Nebula itself no longer does.

Tested: all packages (one old flake, `TestPeerCache_SaveSkipsWhenUnchanged`); `-race` on the Nebula packages, `internal/client` and `internal/mesh`; upstream's own e2e on the patched tree; `TestE2E_Upgrade` from v0.11.28 (150 s, pass); the lab, 30 of 30 with the same silences as before (`concepts/netlab`); on Linux the close tests and the kernel-TUN test.

**Found on Linux only:** upstream's new close looked like our patch 31, so 31 was dropped, and on the Mac everything was green. In the VM `TestKernelTunClose_ReleasesDeviceAndPort` failed 4 of 5 and `TestUserspaceClose_DoesNotExit` every time: upstream wakes the reader and returns, and the kernel keeps the port and the device until the reader has left. Waiting restored (`linux-udp-close.patch`, `linux-tun.patch`): 10 of 10.

**Speed, mini and MBP, 15 s iperf3 each way, three rounds, the same half hour** (`results/perf-20261126-nebula-1.11.2/`): v0.11.28 162 to 181 Mbit/s with 6666 to 10220 retransmits; v1.11.2 165 to 185 Mbit/s with 3631 to 9203; ZeroTier beside both 173 to 188 with 99 to 1569. No difference to be seen; the line is the limit. The Macs run v0.11.28 again.

Pages touched: 2 updated (concepts/netlab, open-questions), this log, patches/README.md, CLAUDE.md, docs/architecture.md.

## [2026-11-26] ingest | v0.11.29 released and deployed; cellular, restart and reboot measured on the two Macs

Nebula v1.11.2 is merged (PR 5, three reviews: no behaviour of the old patches lost), released as v0.11.29 and on the production server and both Macs since 10:27.

**Restart of the mini's agent, MBP pinging every 0.5 s:** clean restart 120 of 120 answers, SIGKILL 118 of 120.

**MBP from Wi-Fi to a phone hotspot (10:49):** both networks direct to the mini again about 2 s after the change was seen, no relay. Speed in 8 s runs (`results/perf-20261126-cellular-v0.11.29/`, not in git): up 24.8 and 29.2 Mbit/s (ZeroTier beside 18.9 and 4.9), down 97.6 and 62.8 (47.9 and 41.7); idle ping 44 ms against 57 ms, no loss. Retransmits 1813 to 17526 against 244 to 959.

**Back to Wi-Fi (11:02):** silent 2.0 s on both networks.

**Reboot of the MBP (FileVault on):** agent stopped 11:05:07, answers again 11:06:09 (`work`) and 11:06:16 (`home`): 62 and 70 s, of which the mesh itself took about a second per network once the agent ran. The three networks start one after the other, about 3 s each. The agent started before the network was up (two failed handshake sends) and recovered by itself. No CRITICAL, WATCHDOG or panic.

Not explained: three silences of 2.0 s on both networks at once (10:58:16, 10:59:17, 11:06:32) with nothing in either agent's log.

Pages touched: this log, open-questions, raw/others (already in PR 5).

## [2026-11-26] ingest | Follow-ups after v0.11.29 (branch `fix/followups-v0.11.29`, not pushed, not released)

**Found while measuring retransmits:** one UDP send on the MBP did not return for four minutes; the network was dead and could not be closed. [[incidents/2026-11-26-mbp-send-stuck-in-kernel]]. Fixed: a send may wait 1 s (`darwin-udp.patch`), and a network that did not shut down and whose port is still taken ends the agent at once.

**Retransmits:** see open-questions. Numbers of 8 s runs, MBP to mini and back over `home`, test build: up 135 to 178 Mbit/s with 3641 to 6643 retransmits, down 123 to 173 with 1629 to 9085; ZeroTier beside, down, 109 to 167 with 112 to 715.

**Networks start together** at boot (`Client.Start`), not one after the other; one with a busy port no longer holds the others for its 25 s of retries. `TestStart_NetworksStartTogether`. On the MBP the three networks came up in the same second (11:57:57); before, about 3 s apart.

**Broadcasts are dropped** (`tun.drop_local_broadcast`, set by `ensureP2PConfig` on every start), so no more handshakes with `x.x.x.255`. `TestEnsureP2PConfig_DropsLocalBroadcast`. The setting is in every network's config on both Macs; that the handshakes are gone needs a network change to show.

**Gate:** `make test-race`, `make preflight` (vet, tests, race), and `make release` runs it first and refuses a dirty tree. CI: a `race` job on Linux and on a Mac (the vendored macOS tests ran nowhere before); the dead `check-patches` job is gone. The release workflow builds nothing when the tests fail. The workflows have not run yet: the branch is not pushed.

Pages touched: 1 new (incidents/2026-11-26-mbp-send-stuck-in-kernel), index, open-questions, this log, patches/README.md, CLAUDE.md.

Both Macs run this branch as a test build (v0.11.29-followups, 2e88874) since 11:57; pings both ways without loss. The release binary is beside it in `~/hopssh-saved-2026-11-25/`.

## [2026-11-26] follow-ups, second round: the loss on the line, the router-reboot test

**Loss of our packets on the line is the line's limit.** Capped sender: 0 lost at 60 Mbit/s, up to 0.5 % at 100, 1.3 % at 140; uncapped 173 to 185 Mbit/s with 2 to 3 %. ZeroTier beside it stops at 122 to 167 and loses none. Not the bursts (test builds with the tunnel reader's batch at 4 and 1 against 32, alternating, no difference), not fragments (none). Nothing changed in the code.

**`TestRouterReboot` failed in the full lab run (60.9 s, limit 45 s), 29 of 30 passed.** It is TCP's retry timer, not the path: see open-questions 2026-11-25, closed. The test now asks with a new connection after the router is back (limit 5 s; measured 0.1 to 1.2 s in 4 runs) and allows the open session 65 s.

**Recorder for the 2.0 s silences** runs on the MBP since 12:19: `ping -i 0.5` over `home`, `work` and ZeroTier, into `/tmp/hoprec/*.log`.

Pages touched: open-questions, this log. Both Macs are back on v0.11.29-followups (2e88874) after the test builds.

## [2026-11-26] follow-ups, third round: the MBP on the hotspot (13:15 to 13:54), the recorder

Build v0.11.29-followups on both Macs. The MBP was reached through the mini (`ssh -J`), ZeroTier from the third machine found no path to it on cellular.

- **The switches.** To the hotspot (13:15:43): `home` missed one ping of the recorder (0.5 s apart), `work` none, ZeroTier 88 (44 s). Both networks were direct to the mini again at 13:15:46. Back to Wi-Fi (13:53:59): ours 40 pings (20 s), ZeroTier 94 (47 s); the agent logged the change at 13:54:21. How much of the 20 s was the Mac joining the Wi-Fi is not known.
- **Broadcasts:** no handshake with an `x.x.x.255` address after the start of this build (last one 11:57:12, the old one), through both switches. Closed.
- **"Flush failed":** six lines in the second of the first switch ("can't assign requested address", "cannot allocate memory"), none else. No send ran into the 1 s limit.
- **Speed, 8 s, one run each:** hop up 20 Mbit/s (6153 retransmits), down 54 (1327); ZeroTier up 2 (259), down 33 (891). **The upload numbers are not valid:** a Screen Sharing stream from the MBP over ZeroTier was using the uplink at about 5 MB/s the whole time (`nettop`: `avconferenced`, `zerotier-one`). The "udp lost" column of the script counts all UDP of the machine and was drowned by it. A second try came after the MBP had left the hotspot. So the cellular upload retransmits (17526 on 2026-11-25) are still open.
- **Nothing fragmented** on cellular (0 fragments received on the mini).
- **Recorder, 12:19 to 14:05:** no silence of 2 s without a cause. Single pings lost on `home` and `work` in the same instant three times on cellular (13:22:19, 13:32:19, 13:45:08), nothing in the log. Two pings lost on `work` at 14:01:55: its cert renewal, which restarts the network (about 1 s). 13:11:13: all three, ZeroTier too, so the line.

Pages touched: this log, open-questions.

## [2026-11-26] follow-ups, fourth round: cellular upload, measured cleanly

MBP on the hotspot again (about 14:30), nothing else sending (56 UDP datagrams a second before the runs). 8 s uploads to the mini:

| run | speed | retransmits | our UDP lost on the way |
|---|---|---|---|
| hop, uncapped | 27 Mbit/s | 4194 | 3284 of 25666 (13 %) |
| hop, uncapped, again | 17 Mbit/s | 1026 | 762 of 14744 (5 %) |
| hop, capped at 10 Mbit/s | 10 | 46 | none |
| hop, capped at 5 Mbit/s | 5 | 161 | none |
| ZeroTier, uncapped | 11 Mbit/s | 480 | none |

Same as on Wi-Fi: the loss is the line's limit. TCP inside hop pushes the cellular uplink past what it takes and loses 5 to 13 % there; ZeroTier limits itself below that and is 1.5 to 2.5 times slower. No tunnel drops, nothing fragmented. The 17526 retransmits of 2026-11-25 are this. What would lower them is a queue on our side that holds TCP back before the line does (pacing, or dropping early like fq_codel); not built, see open-questions.

Pages touched: this log, open-questions.

## 2026-11-26 (fifth round) — the loss is the start of a connection; an early signal tried and removed

- Per second of the earlier runs: nearly all retransmits in seconds 1 to 3 (the TCP window goes to 2 to 3 MB, then to 0.5 MB), none after. Counted at both interfaces on the hotspot: 22456 packets left the MBP, 21722 reached the mini.
- Lab: `Wire.Queue` (how deep the line's queue is) and `TestLimitedLine`. A sender that doubles until loss loses 0.1 to 1.5 %, more with a deeper queue, the same through the tunnel and beside it. The test is a gate now (the tunnel may not lose more than twice as much, nor be below 70 % of the speed); a planted 1 % drop fails all five lines.
- Prototype in the vendored tree (not kept): test requests with the time in them every 32 packets, one TCP packet held back when the answer took 20 ms longer than the shortest. Lab: helps only with a 200 ms queue. Macs on Wi-Fi: retransmits 54, 21, 20 instead of thousands, speed 69, 37, 50 instead of 180 Mbit/s, download 3.9. The Wi-Fi's own pauses of about 100 ms set it off. Removed with `make vendor`; both Macs are back on v0.11.29-followups (control run: 147 Mbit/s, 3939 retransmits).
- `networkQuality -d` on the hotspot: 31 Mbit/s up. It does not count in `netstat -s -p tcp`.

Pages touched: concepts/loss-on-a-limited-line (new), open-questions, index, concepts/netlab.

## 2026-11-26 (sixth round) — Tailscale beside us; a filter on the MBP caps our download

- Tailscale started on the mini for the comparison (the user signed in), stopped again afterwards.
- Upload, three rounds: hop 179 to 186 Mbit/s with 708 to 3158 retransmits, Tailscale 172 to 178 with 1112 to 2837, ZeroTier 165 to 192 with 455 to 1028. Tailscale has the same burst at the start.
- Download: hop 34 to 65, Tailscale 157 to 161, ZeroTier 91 to 180. Cause found on the MBP: Microsoft Defender's network extension inspects hop's packets (56 % CPU) and not ZeroTier's. Ruled out: path, packet size (MTU 1280 to 1420), the line, Tailscale, the mini.
- Open: why it was fast at noon.

Pages touched: concepts/loss-on-a-limited-line, concepts/defender-filter-on-the-mbp (new), open-questions, index.

Correction the same evening: a repeat showed Defender's filter as busy during a ZeroTier download that was still fast, so the filter is a suspect only. The downloads are slow since the MBP rejoined Wi-Fi at 13:54, perhaps on another office network. Page and open question reworded.

20:14: the MBP switched to the office's other Wi-Fi name: same gateway and subnet, hop download still 36 to 38 Mbit/s, ZeroTier 180 to 186. The Wi-Fi name is not the cause.

20:29 to 20:45: the slow state ended by itself (hop download 145 to 179 Mbit/s, Defender's filter at 3 to 9 % CPU where it had been at 50 to 100 %). Found before that: capped at 20 and 40 Mbit/s the slow state passes clean; a test program could not make the filter busy by any way of using a socket; our send buffer is 9 kB, ZeroTier's 1 MB. A test agent with 1 MB was fast, and so was the unchanged one three minutes later: not shown, not in. The send limit of 1 s was seen ending 106 stuck sends at 20:1x to 20:2x. A test agent with 1 MB on `work` only is on the MBP and a watcher compares the two networks every 10 minutes.

Pages touched: concepts/defender-filter-on-the-mbp (rewritten), incidents/2026-11-26-mbp-send-stuck-in-kernel, open-questions, index.

21:37 to 21:45: the slow state came back for five minutes. Side by side in it: `home` (9 kB send buffer) 36 to 43 Mbit/s down and 170 up, `work` (1 MB) 9 to 10 down and 55 to 87 up, ZeroTier 129 to 191. The big buffer is ruled out. Six provoked rebinds did not bring the state on; screen sharing does not line up. The MBP is back on v0.11.29-followups (2e88874), the watcher has ended.

22:00 to 23:00: the recorder read (12:19 to 20:32). One minute of silence on `home` at 20:09:54, every send into the 1 s limit while receiving went on; the agent now replaces a socket that takes nothing for 5 s (`send_watch.go`, `SendStuckFor` in two patches). The 20 s on hotspot to Wi-Fi is the change itself (ZeroTier: 47 s). The short silences stay unexplained. The 1 in 30 failure of `TestPeerCache_SaveSkipsWhenUnchanged` fixed (both saves in one second of the clock). Recorder stopped. PR 6 opened; Actions minutes are not billed for this repo, it is public.

Pages touched: incidents/2026-11-26-mbp-send-stuck-in-kernel, open-questions, index.

22:12 to 22:40, Linux: the whole lab on the tip. All pass but `TestLimitedLine/5ms` (8 of 8 runs, with and without the new loop; passed at 16:00). Filed in open-questions. CI on PR 6 green on `75f5c50`, macOS and Ubuntu.

23:27, fleet: 24 minutes after the start of v0.11.30 both Macs have 0 lines of `send-watch`, `Flush failed`, `CRITICAL`, `WATCHDOG`, panic, rebind and failed heartbeats; pings MBP to mini 20 of 20 on both networks. The orphan node of the mini in `member-test` was deleted in the dashboard.

23:30 to 00:10, Windows 11 ARM64 in UTM, agent v0.11.30 against this tree's server on the host's bridge address (commands through `utmctl exec`, files through `utmctl file`). Works: token enrollment, service, tunnel, name lookup, restart. Found and fixed on `fix/windows-vm-findings`:

- `enrollWithToken` sent `"os":"linux"` on every system (`TestTokenEnrollBody_ReportsThisSystem`).
- The line `HOPSSH_LOCAL_API:<addr>:<token>` went to stdout, which for the system service is its log: `/var/log/hop-agent.log` is readable by everyone on the Macs, `hop-agent.log` by `Users` on Windows. With the token a local user can enroll the root agent into a network of their own and open a terminal as root. Now only into a pipe, which is what the desktop app gives the agent it starts (`TestAnnounceLocalAPI_*`). The Go tests read it through a pipe already; `clients/desktop/scripts/smoke-test.sh` was changed to.
- The split-DNS setup ran before the watcher, the path loop, the mesh listener and the local API. On Windows it starts PowerShell twice; in the VM they came 98 s after "mesh connected" on two starts, 5 s with the setup beside the rest. No test: the tests run in userspace mode, where there is no DNS setup.

Pages touched: open-questions.

00:15 to 00:25, the Ubuntu 26.10 VM joined as second device (no root there: userspace mode, started by hand). Windows to Linux: direct handshake from the Windows VM's own address, ping 4 of 4, lookup by name. Through the server: health and exec on both, a terminal on both (ConPTY on Windows: prompt, a command and its output came back). Forced renewal: Linux at once, Windows 9 s, both reachable after. The Linux agent's log has no token line (the fix).

Pages touched: open-questions.

01:00, the Ubuntu VM again, now as root with the service: kernel TUN `hop-hop`, split-DNS (the resolved drop-in fallback fired, as on 24.04 and 25.10), ping and lookup by name both ways. Restart: the Windows peer answers after 2 s. Forced renewal: at once, no "busy" or "in use". Stopped for 40 s: "sleep/wake detected (tick gap 40s)", peer answers 2 s after. No token line in the journal.

Pages touched: open-questions.

01:05 to 01:15, lab: `TestLimitedLine/5ms` three times on the tip and three times with the binaries of `bf557ea`: fails with both. The host's load average was 31 on 18 cores. Filed in open-questions; the test is unchanged.

Pages touched: open-questions.

01:55, lab: with the host idle `TestLimitedLine/5ms` passes three of three (13.0 to 14.0 against 16.1 to 16.3 Mbit/s). It was the host's load.

02:05 to 02:25, the one Windows line left unexplained: `Failed to write to log, write /dev/stderr: The handle is invalid.`, once per start. It is logrus's own complaint, not ours. The port mapper logs with logrus (`internal/portmap/portmap.go:54`), and logrus took `os.Stderr` when the process started; a Windows service has no console handles, so the write fails. Every `portmap:` line was lost, and the log got the complaint instead. Fix on `fix/windows-vm-findings`: `routeStandardLog` points the standard logrus logger at wherever the standard log writes when the line is written, so it follows the service's redirect (`TestStandardLogrus_LandsInTheAgentLog`, `TestStandardLogrus_FollowsTheLogWhenItMoves`; red before the fix, green after). In the VM with the new binary: the `portmap: no protocol available` line is in `C:\ProgramData\hopssh\hop-agent.log`, and no failed-write line after that start (the start two minutes earlier, old binary, still had one). Nebula's own lines were already fine: `nebulacfg.Logger` sets its writer.

Pages touched: open-questions.

02:30 to 02:40, two networks at once on the two VMs. A second network `vmtest2` (domain `.lab`) on the private server, both VMs enrolled into it. Windows: both interfaces up (`hop-hop` 10.42.1.3 on 4242, `hop-lab` 10.42.2.2 on 4243), each network with its own DNS forwarder on its own loopback (`127.53.0.1:53` → 15300, `127.53.0.2:53` → 15301) — a query for `.lab` sent to `.hop`'s forwarder is refused, which is that design, not a fault; NRPT sends each domain to its own. Traffic passes both ways on both networks, and a restart of the service brings both back within 6 s. Linux: the second enrollment did **not** come up at all — `installAgentSystemd` ran `systemctl start`, which does nothing to a unit that is already running, so the new network waited for a reboot. macOS (`bootstrap` after `bootout`) and Windows (stop + start) already restart what is running. Fix: `serviceStartCmd(active)` asks with `systemctl is-active --quiet` and restarts then (`TestServiceStartCmd_RestartsARunningService`, `TestInstallAgentSystemd_RestartsWhatIsRunning`; the tripwire fails on the old installer). With the fixed binary in the VM, `hop-agent install` with the service running brought `lab` up in the same minute (interface, listener, path loop, split-DNS) and both networks passed traffic both ways. Seen once and not explained: `[dns hop] WARNING: failed to cleanup DNS for .hop: remove NRPT rule: exit status 0xc0000409` — the PowerShell that removes the rules died; the next start re-registers them.

Pages touched: open-questions.

02:40 to 03:05, the two VMs again: a change of network and sleep, both of which had never run on Windows. Windows: an address change on the adapter (`netsh interface ip set address`) — `network change detected, rebinding Nebula` at 02:54:54, both networks rebound within 2 s, the tunnel to the Linux VM answering again 4 s later, about 5 s of pings lost. Sleep: the process frozen with `NtSuspendProcess` for 40.4 s (the VM has no sleep states, and this is the SIGSTOP equivalent the log already prescribes) — `sleep/wake detected (tick gap 44s)`, rebind, an out-of-cycle heartbeat, on both networks; pings were lost exactly during the freeze and 0 % after the resume. Linux: an address change recovered in about 4 s the same way. Do not use `netsh interface set interface "Ethernet" admin=disable` in that guest: the adapter never comes back and the guest agent dies with it; only `utmctl stop` + `utmctl start` recovers, and the address change tests the same path.

03:05 to 03:35, CI on this branch, two red jobs, and the same question twice: a UDP port "still held" after a close. What the logs actually said: `race (ubuntu-latest)` died with `hop: network "home" did not shut down and still holds UDP port 35997 after 1s — exiting (75)`, which is `restartInstance`'s own escalation (`recovery.go:219`) killing the test binary; `build` failed inside `TestUserspaceClose_DoesNotExit` at cycle **0** of 10, on port 54695. Both ports are in the ephemeral range, and both tests ask once, at one instant. Measured in the Ubuntu VM: 300 real start/close cycles of userspace Nebula, with four goroutines hammering the hostmap so the reader is always in its syscall — the port was still held **0 of 300**, and `/proc/net/udp` showed no socket left. The patch's give-up warning (`udp reader did not exit`) appears in no CI log, so the readers exited and the closed socket was gone. What was left holding the port, then: another socket. `go test ./...` runs packages in parallel, `cmd/agent` and `internal/e2etest` bind UDP ports too, and Nebula sets `SO_REUSEPORT` on Linux (`vendor/…/udp/udp_linux.go:46`), so a second instance binds a port that is already taken and neither one fails. The sibling helper `freeUDPPorts` already knew this and picks "below the ports the OS hands out"; `freeUDPPort` asked the kernel instead. Fix: one helper `reserveUDPPorts` for both, from the 20000–29999 band, and no port handed out twice in one test binary (`TestFreeUDPPort_NotFromTheEphemeralRange` is red on the old helper — it returned 63223 — and green on the new). Both assertions also stop glancing: `within(t, 10s, …)` waits for the port to be free and for the reconnected network to be live, and says how long it took. Nebula's close gives each reader a second before closing the socket, so "free" is a wait, not an instant. 20 of 20 and 15 of 15 on Linux after, and the whole `internal/client` package green there. **Still open:** the other failure in the same run, `TestE2E_DisconnectDuringRenewal` at `recovery_e2e_test.go:284` — `reconnect: err <nil>, live false`, so `Connect` returned no error and 100 ms later the instance was not live. `Connect` returns as soon as `startInstance` succeeds; something closed it in that window, most likely a renewal that was already in flight when the network was disconnected. Not reproduced (15 runs on Linux, 8 under `-race` here); the wait now covers it, and the mechanism is unfixed.

Pages touched: open-questions.

03:40 to 04:05, the last Windows path never run: self-update. The VM's agent was replaced with one built from this tree and stamped `v0.11.28`, the private server on the Mac reports `v0.11.30`, and `hop-agent update` on the VM did the whole thing: `Update available: v0.11.28 -> v0.11.30`, download from `/download/hop-agent-windows-arm64.exe`, SHA-256 verified, the running `C:\hop\hop-agent.exe` renamed to `.old` (Windows allows that, not an overwrite), the new binary placed, `sc stop` + `sc start`. `version` then says `v0.11.30`, the service is RUNNING, and both meshes are back: 0 % loss Windows→Linux and Linux→Windows on both networks, 1-3 ms. One thing to know: the `.old` (24 MB) was still there after the update — `cleanupOldBinary` runs at the start of `runServe` and its error is discarded, and the SCM can still hold the image of a service it just stopped. The next start removed it. So the leftover is one cycle, not permanent, and nothing tells you about it.

04:40 to 05:05, the flake that was not in the product. `TestE2E_DisconnectDuringRenewal` failed 9 times in 10 on the Mac at the line that glances at `live()` after `Connect`. The failure text was the answer all along: `TempDir RemoveAll cleanup: lstat …/home/node.crt: directory not empty`. `t.TempDir` removes the instance directory when the test ends, and a renewal POST already on its way still writes `node.crt` and `node.key` after `Stop` cancels its loop — the removal and the write race, and the test died in teardown, not in the assertion. The wait for `live()` that an earlier session added made the test quiet without fixing anything, and it hid the real question. Fix in the harness: `newE2EMesh`'s own cleanup calls `Stop` and then waits for the last `runCertRenewal` frame to leave the goroutine stacks (30 s, and it prints the stacks if one stays), so the directory is empty before `TempDir` removes it. The assertion is a glance again, so a reconnect that comes back wrong is still a failure. 10 of 10 green on the Mac. The `live false` from CI on 2026-11-27 00:36 stays unfound: 15 Linux runs and 8 under `-race` here, no reproduction. The only mechanism found that could produce it is a reload closing the running Nebula before starting the new one (the v0.10.33 ordering), which leaves `control()` nil for up to 2 s of port-free wait plus the start — but nothing in that test fires a renewal within 100 ms of `Connect` (the loop's first check is one 60 s tick in), so it stays a guess and is not claimed as the cause. The test now waits out a swap of that size (live within 5 s) and checks it is still live 3 s later: the glance passed 10 of 10 here, so the wait costs nothing, and a reconnect that never comes back or comes back and dies now fails instead of passing.

05:05 to 05:25, and the pair race that failure had been circling. Nebula opens `node.crt` and then `node.key` (`vendor/github.com/slackhq/nebula/pki.go:307`, `:320`) and refuses the combination with `ErrBadPrivKey` if they do not belong together. The renewal loop is already running while `connect` holds `startMu` and Nebula reads, so a renewal landing in that window left the new cert next to the old key: `Could not load client cert … private key is not a pair with public key in nebula cert`, the whole start fails, and the network falls to the OS stack with Nebula only on the retry. `certInstallMu` only ordered renewals against each other. Fix: `writeCertPair` takes `inst.startMu` for both writes; both writers (the renewal loop and the pre-start `fetchAndInstallCert`) go through it, and the only other `node.crt` writes are enrollment-time, into a directory with no instance. Tripwire `renew_pair_test.go::TestRenewalWaitsForTheStartToReadThePair`: red 3 of 3 without the lock, green 3 of 3 with it. Neither Mac in the fleet has logged a "not a pair" line, so the window is common to hit, not one that has been seen.

05:25 to 05:40, three small things. (1) `reloadNebula`'s cold branch — no Nebula running, which is what a failed boot start leaves — logged the failure and returned, while the hot swap schedules retry-with-backoff. The next chance was the next renewal, about 13 h later on the 24 h certs, for a failure that is usually the UDP port still held by the start that failed at boot. Now both branches call `scheduleRetryReload` (`TestReloadNebula_I6_ColdStartFailureIsRetried`: red with the call removed, green 10 of 10 with it). The retry starts in the TUN mode it was asked for (`startNebulaByMode` has no userspace fallback, unlike `connect` and the cold start) — left as it is, and the comment that claimed otherwise corrected. (2) The mini's 10 log lines containing "429" are coincidental substrings: Nebula's handshake indexes, `.429` timestamps, `ticks: 4291`. No HTTP 429 anywhere in its log, so the agent limiter (600/min) is not being hit. (3) `govulncheck ./cmd/server ./cmd/agent` on v0.11.30 found one thing: **GO-2026-6278** (GHSA-w67g-5rqw-f597, CVSS 6.9) — gorilla/websocket picks WebSocket frame **mask keys with `math/rand`**, so a peer on the path can predict the mask and inject frame content. We use gorilla on both sides: the browser terminal and event stream in, and the control plane dialing an agent's shell out (`internal/api/proxy.go:271`, `:938`). `v1.5.3` is the newest tag and still has it — that tag's note is "Reverts to v1.5.0", which undid the v1.5.2 fix — and the fix is on master only (`d67f41855da4`, "Use crypto/rand for mask key"), so the fixed version is the pseudo-version the vuln DB names. Pinned there on `deps/gorilla-mask-key` (PR 9): build, vet, `TestE2E` in `internal/api`, `-short` for `internal/api` + `internal/client` + `cmd/agent`, `make vendor` (all 10 patches apply) and `make test-race` green, `govulncheck` clean. The behaviour changes in the 14 commits between were checked against our code: a request with no `Upgrade` header now gets 426 from `Upgrade()` instead of 400, but our handlers answer that case first, so `internal/api/e2e_test.go:153` still sees 400; a client that offered subprotocols and got none now fails the handshake, and we offer none anywhere.

Pages touched: cert-renewal, open-questions.

05:40 to 06:00, PR 9 (the pinned gorilla/websocket) on the path it guards, not only under `go test`. The browser terminal is the only place we use gorilla inbound, so the pinned build was driven through the real thing: the private server on the Mac (built from `deps/gorilla-mask-key`), the Windows VM's agent, and a dependency-free probe (`$S/vm/wsprobe.py`) that sends the real `Upgrade` request with the dashboard's session bearer token, checks `Sec-WebSocket-Accept` against the SHA-1 of its own key, then hand-rolls masked client frames. Result: `HTTP/1.1 101`, accept correct, 10 text frames, and a real PTY round-trip — the probe typed `echo PIN$(echo CHECK)_OK` once the agent's banner arrived and read back `PINCHECK_OK`, which is what the shell printed, not what was typed. That last detail is the lesson: the first version of the probe asserted a marker that was also in the command it typed, so terminal echo alone made it pass. Assert a marker the shell has to produce.

06:00 to 06:25, why the Windows VM was listed with the Linux icon, and what now fixes it. Only enrollment ever wrote `nodes.os` and `nodes.arch`, and the token path guessed: `internal/client/enroll.go` posted a literal `"os":"linux"` in the token-enroll body (the device flow already used `runtime.GOOS`), and the server stores `body.OS` as given — it never substitutes its own. `scripts/install.sh` pipes the token to `hop-agent enroll --token-stdin`, so every machine installed that way was listed as Linux, whatever it was. `RecordHeartbeat` (`internal/db/nodes.go`) never touches those columns, so a wrong value is permanent. The agent-side fix (`72f2165`) is on `fix/windows-vm-findings` only — it is **not in v0.11.30 or any tag**, so released agents still enroll wrong. What corrects them is this: the heartbeat body now carries `runtime.GOOS` and `detectArch()`, and the handler writes them through `NodeStore.SetPlatform` when they differ from the row (the beat is every 30 s per node, and this handler is deliberately write-free otherwise). `COALESCE(NULLIF(?, ''), ?)` because an agent before v0.11.31 omits the fields — no opinion must not erase what a newer agent reported. Tests, red before the fix: `TestSendHeartbeat_ReportsItsOwnMachine` (the body had no `os`), `TestE2E_HeartbeatCorrectsPlatform` (against the real server: a node enrolled as linux/arm64 is windows/amd64 one beat later, and a beat without the fields leaves it). On the real machines: the Windows VM's row on `vmtest` went `os=linux` → `os=windows arch=arm64` within one heartbeat of the new binary, its fresh enrollment on a third network reported `windows/arm64` at once (that agent is built from this branch, so it proves `72f2165`, not a release), and the Linux VM's rows stayed `linux` — the correction is not a blanket overwrite.

06:25, one Windows swap gotcha for the next dev-deploy: `Stop-Service hop-agent` and then `Copy-Item` over `C:\hop\hop-agent.exe` fails with "being used by another process" even though the service reports STOPPED — the SCM still holds the image of the service it just stopped. The rename-then-copy that self-update already uses (`hop-agent.exe` → `.old`, which Windows allows for a running image) is what works; `cleanupOldBinary` removes the `.old` on the next start.

Pages touched: log, concepts/e2e-tests.

07:00 to 07:40, the fleet on the fixed code, and what our firewall really opens.
(1) **Production is on everything that was broken.** The Nomad job runs
`ghcr.io/trustos/hopssh:v0.11.30` and sets `HOPSSH_NODE_PRUNE_AGE_HOURS = "0"`, so
the control plane no longer deletes a node for being quiet (cause L). Both Macs run
agent v0.11.30. The renewal deadline is now a countdown to a fixed instant, read
live on the mini: `due in 20m21s`, then `19m21s`, `18m21s`, `13m21s` (cause E). A
watcher is waiting for the mini's `home` renewal to land so the reload can be seen
in the log, not only scheduled.
(2) **The office loss is not "the office network".** In the worst hour `home` lost
9 % in both directions between the two Macs while `work` between the same two lost
nothing; over 40 hours 0.59/0.63 % against 0.16/0.09 %. Corrected in
[[concepts/loss-on-a-limited-line]] and reopened in [[open-questions]]. The lab pair
(two networks on the same two VMs, 1000 pings each) is clean, so it is not our
two-network plumbing either. What would settle it is a packet count on the wire
during a lossy minute; the run that does it (5 pings a minute per network for 40
minutes, counting pings, wire packets and the agent's unfiltered log together) was
refused by the permission classifier on the office Mac and needs the user's OK.
(3) **The UDP rule protects nothing the TCP rule has not already given away.** Read
off the live `nebula.yaml` on the mini: inbound is TCP on every port from the `node`
group, TCP 41820 from `admin`, and ICMP — no UDP. `hop client join` allows inbound
ICMP only, so a joined client accepts nothing from the mesh. The control plane's own
lighthouse allows all three protocols. So opening UDP between nodes costs no
exposure that all-TCP does not already allow; firewall rules (roadmap #13) are the
thing that would actually restrict anything.
(4) `govulncheck ./...` on this branch: one reachable finding, the gorilla/websocket
one that PR 9 pins. Nothing else.

07:43, the renewal seen in production, and the release gate for v0.11.31.
(1) **A renewal on the mini, end to end, in the log.** `home`, agent v0.11.30,
kernel TUN: `07:42:06 cert OK, renewal due in 21s` → `07:43:06 cert needs renewal
NOW (past half its life)` → `attempting renewal POST` → `stopping Nebula mesh
connection (kernel TUN)` → `Nebula interface is active interface=utun6
networks=[10.42.1.2/24] udpAddr=0.0.0.0:4242` → `Nebula restarted with new
certificate (mode: kernel)` → `HTTP server restarted on new Nebula mesh listener`
→ `certificate renewed successfully` → `network-change watcher started`. No
"address already in use", no retry line, no CRITICAL, no watchdog trip. That is
cause E (a fixed renewal instant, counted down in the log) and the close-before-
start reload of v0.10.33 working on a real machine, not only in a test.
(2) **`TestE2E_Upgrade` against the released binaries: PASS (140.18 s).** Built
v0.11.30's `hop-server` and `hop-agent` from `refs/tags/v0.11.30` in a worktree
(`go mod vendor` + `make patch-vendor`), with `PortmapEnabled = false` in the old
tree so an old agent cannot touch the router of the machine running it. Old server
+ old agent enroll, then the new ones take over, then two forced renewals and a
ping. Helper: `$S/field/build-old.sh`.
(3) **`make preflight` green on `fix/windows-vm-findings`** (vet, `go test -short
./...`, and `-race` over `internal/mesh`, `internal/client` and the vendored
nebula + nebula/udp). Remaining release item: the same tests on Linux (rule 13).

08:20, the client's tests as root on Linux, and the release gate.
(1) **Five path tests fail as root on real Linux and pass as an ordinary user on
the same machine** (and on the Mac): `TestE2E_NewTunnelStays`,
`TestE2E_StartsWhereBothHear`, `TestE2E_MeetOnTheLAN`,
`TestE2E_QuestionMovesNobody`, `TestE2E_NoAnswerToADeniedAddress`. Their numbers
were the ones that look like a path bug: `still sent to 127.0.0.1:9 after
1.000354208s`, `no tunnel to 10.42.1.2 in 3s`, `first answer after 1.02s, want
under 0.8s`. Nothing of ours is wrong: `readTunMode` upgrades a network to kernel
TUN whenever the process is root, so the harness asked for userspace (`tun:
{user: true}`, a `tun-mode` file saying userspace) and quietly got a real TUN
device, while every peer in these tests lives on a 127.0.0.x address, which only
the userspace netstack answers. Found only because rule 13 was followed and the
package was run as root on Linux for the first time.
(2) **Fix:** `tunUpgradeAllowed` in `internal/client/nebula.go` (a variable;
production is `isPrivileged`), used by `readTunMode` and `currentTunMode`;
`newE2EMesh` refuses the upgrade for its own process. Tripwire
`TestE2E_RootStaysInUserspace`, which fails if the harness ever starts a kernel
device. Green in the Ubuntu VM as root: the five pass again (5.60, 5.61, 12.08,
11.13, 19.62 s) and the tripwire passes (5.51 s).
(3) **A start failure took the whole package's results with it.** In
`TestE2E_NewTunnelStays` the `tunnel()` helper dereferenced the hostmap entry
without checking; when there was no tunnel it panicked, and a panic in a test
binary stops every test after it, so the run reported one crash instead of the
five failures that were there. `tunnel()` now fails its own test.
(4) **The release gate had holes; now it is what a laptop runs.** `make
test-race` (the only `-race` run over the Nebula code we patch) was in no release
path, the kernel-TUN test (which found V, W and X) was in none either, and
`release-desktop.yml` built shipped Windows and Linux binaries in jobs that waited
for nothing. `release.yml`'s test job now runs `make preflight` plus the
kernel-TUN test as root in a fresh network namespace, and all three desktop build
jobs need `preflight`. Tripwire: `internal/cigate/workflow_test.go`, red with 4
findings before the workflows changed, green after.
(5) **What `-race` over the vendored Nebula actually runs.** `go mod vendor`
carries no upstream test files, so the only tests in those packages are the ones
our patches add: `hostinfo_remote_race_test.go`, `remote_list_relays_race_test.go`
and the hostmap/control testers in the root package, and in `nebula/udp` only
`udp_darwin_batch_test.go` and `udp_darwin_listenout_pipeline_test.go`. On Linux
and Windows the `nebula/udp` argument therefore compiles and runs nothing — the
batched UDP and TUN code is tested on a Mac only, as CLAUDE.md already says.

09:05, the client package as root on Linux, end to end.
(1) After the userspace pin, `internal/client` as root on the Ubuntu VM is
green: 1089.91 s, 3 skips, no failure. The kernel-TUN close test as root in a
fresh network namespace passes there too (2.30 s) — the pair CI's release gate
now runs.
(2) **One tripwire could only ever pass on the machine that built it.**
`TestInstallAgentSystemd_RestartsWhatIsRunning` found `service.go` with
`runtime.Caller(0)`, which returns the path as the compiler saw it; run from the
VM it read `/Users/tenev/…/service.go` and failed. It was the only source scan in
the repo written that way — the other 27 use a path relative to the package,
which is also why they failed loudly (not silently) when I first copied the test
binary to the VM without its tree. Now relative like the rest. A scan that reads
a source file must name it relative to the package, or it tests the build
machine, not the test machine.
(3) **`internal/api` and `cmd/agent` cannot be run as root in that VM**: their
end-to-end tests build `hop-server` and `hop-agent` themselves (`go build
./cmd/server`), and the VM has no Go. They ran as root in the podman
`golang:1.26` container instead (no `/dev/net/tun` there, so the agent stays in
userspace). Running them as root on a machine with a real TUN device needs a Go
toolchain in the VM — not installed, because that is a download.
