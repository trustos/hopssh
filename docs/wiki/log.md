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


## [2026-05-10] ingest | Linux desktop first-user-test surfaces stuck-onboarding + Load-failed (open incident) + v0.11.18 platform-neutral copy

Two strands. (a) **First Linux .deb desktop client user-test (aarch64 Ubuntu VM)** surfaces two symptoms: onboarding stuck at step 2 even after device-flow approval, and manual Connect later returns "Load failed" (JS `TypeError: Failed to fetch`). Disk state confirms enrollment IS persisted; both `hopssh-desktop` + `hop-agent` PIDs alive at user's snapshot; journalctl excerpt has Go stack frames at `internal/client/client.go:87` (the `_, err := try()` line in the 4-attempt connect retry) — most likely a panic but the panic message itself wasn't captured before the diagnostic SSH session ended at precompact. Filed as open incident `incidents/2026-05-09-linux-desktop-stuck-onboarding.md` with hypotheses ranked + next-investigation steps; mirrored in `open-questions.md`. **Action**: SSH back to the VM (creds in `e2e-connections` repo), capture the panic message, root-cause from there. (b) **v0.11.18 platform-neutral copy** — three remaining "Mac"-only strings in the dashboard frontend (`/device` authorize page subtitle, network-detail Desktop badge tooltip, sidebar Get-Desktop tooltip) plus one in `internal/client/client.go:109` (port-conflict error → user-visible toast) flipped to "device" / platform-neutral phrasing. Tripwire test in `parallel_install_test.go` updated to match. Linux + Windows desktop clients are first-class as of Stages 5+6 (v0.11.13+); the dashboard copy needs to keep up. v0.11.18 is dashboard + agent only — the Linux Connect crash root-cause investigation is separate work.

Pages touched: 1 created (`incidents/2026-05-09-linux-desktop-stuck-onboarding.md`), 2 updated (`open-questions.md`, `index.md`). Plus 4 source files committed (3 frontend + 1 client.go + 1 test).


## [2026-05-09] ingest | Phase NN — `internal/client/` substrate extraction (v0.11.5)

Extracted ~22k LOC of client-lifecycle code (89 files) from `cmd/agent/` (which was `package main`) into a new `internal/client/` package. The package exposes a gomobile + Rust-FFI compatible public API surface (`Client`, `Config`, `EnrollOptions`, `Snapshot`, `EnrollmentSummary`, `PeerInfo`, `Event`, `EventCallback`, `SubscriptionID`, `InstanceHTTPHook`) with three reflection tripwires in `internal/client/api_constraints_test.go` enforcing no `chan`/`func`/`interface{}` in exported struct fields. `cmd/agent/` reduced to 9 thin-shell files (main.go runServe constructs `*client.Client`, hands a `BuildHandler` hook for the per-instance HTTP listener). Local-API HTTP server moved into `internal/client/` too — its handler-internal coupling to the (now unexported) registries made the originally-planned in-place rewrite a 3-5× larger refactor than the move-into-package alternative; mobile gomobile bindings dead-code-eliminate the unused HTTP server. Three watchdogs (renewal/keepalive/watcher) preserved with stamp-ordering invariants intact; `restartFn` now closes over `c.connect(name)` instead of the runServe-local `connectFn(name)`. Validation: `go test ./...` green; `make build-all` produces working `hop-agent v0.11.5` + `hop-server v0.11.5`; all 41 cargo tests in `clients/desktop/src-tauri/` pass; dev-deployed to mini + MBP, mesh handshake completes ~30s post-restart, ICMP through utun: 6-9ms RTT 0% loss, NAT-PMP mappings established, no CRITICAL log lines.

This unblocks Phase NN+1 (`clients/mobile-go/mobilehop/` gomobile binding), NN+2 (iOS NEPacketTunnelProvider), NN+3 (Android VpnService) — all three were paper-blocked on the substrate per the iOS + Android ADRs. ADR statuses flipped from `proposed/substrate-blocked` to `substrate-built` with progress-checklist marking the substrate done and the gomobile binding + VPN-extension scaffolding as the remaining open work.

Pages touched: 0 created, 6 updated (`docs/wiki/decisions/client-ios-architecture.md` + `client-android-architecture.md` status + path citations; `docs/wiki/concepts/client-strategy.md` status snapshot + project structure; `docs/wiki/concepts/cert-renewal.md` + `watchdog.md` source path updates; `docs/architecture.md` watchdog section header). Plus `CLAUDE.md` Discovery Log entry, `docs/development.md` project structure section, this `log.md`.
