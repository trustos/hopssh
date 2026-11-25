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
