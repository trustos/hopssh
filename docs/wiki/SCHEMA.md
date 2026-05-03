# hopssh wiki — schema & operating rules

**Adapted from Karpathy's "compile-raw-into-wiki" knowledge-base architecture (X post 2026-04-03 + GitHub gist 442a6bf...).** This file is the schema: the instruction set for any LLM agent or human that maintains the wiki. Read this before ingesting new material or running a lint pass.

## Why a wiki at all

The hopssh project accumulates knowledge faster than any single doc can hold cleanly:

- Discovery Log entries in `CLAUDE.md` (deep paragraphs per-incident).
- Long-form plans in `docs/` (one per phase or research thread).
- Forensic dumps under `/etc/hop-agent/<network>/stuck-state-*.txt`.
- Performance baselines in `spike/`.
- Memory files in `~/.claude/projects/.../memory/`.
- Plan files in `~/.claude/plans/`.
- Untracked scratch (e.g. `q.md`, ad-hoc Markdown).

By itself each file is fine, but **cross-cutting questions are hard**: "what's the current state of cert-renewal post-sleep on macOS?" requires walking five files and inferring what's still current. The wiki is the compiled answer to that class of question, kept short and deeply linked.

The wiki does **not** replace the code. The code is the source of truth for "how does X work right now". The wiki is the source of truth for "what did we learn, what did we ship, what are the live invariants, what do the machines + networks + benchmarks look like".

## Layers

```
docs/wiki/
├── SCHEMA.md         this file (instruction set)
├── index.md          master catalog by category
├── log.md            chronological compile-pass log
├── raw/              source-of-truth artifacts (LLM read-only here)
├── entities/         machines, networks, accounts, services
├── concepts/         architectural concepts (sleep-wake, watchdog, cert-renewal)
├── phases/           per-phase post-mortems / shipped state
└── benchmarks/       perf baselines (cellular cold-start, WiFi LAN throughput)
```

`raw/` is for forensic inputs the LLM should read but never overwrite (e.g. an agent log excerpt, a `pmset -g log` dump, a screenshot transcription, a curl probe). Other top-level dirs hold compiled markdown.

The existing `docs/*.md` files (architecture.md, sleep-wake-plan.md, etc.) are **not moved**. They're long-form reference; the wiki points to them via backlinks. The wiki itself is short pages — one screen each whenever possible.

## Page conventions

Every wiki page starts with YAML frontmatter:

```yaml
---
type: entity | concept | phase | benchmark
title: Human-readable title
status: current | superseded | archived
last_compiled: 2026-05-03
sources:
  - docs/sleep-wake-plan.md
  - CLAUDE.md (macOS Platform discovery log)
  - cmd/agent/renew.go
---
```

After the frontmatter:

1. **TL;DR** — 2–4 lines. The single takeaway.
2. **Body** — short, navigable. Use `[[backlinks]]` to other wiki pages where they help.
3. **Backlinks** — explicit list at bottom of related pages.
4. **Sources** — point at code + raw artifacts that justify each claim.

Backlinks use `[[wiki-page-name]]` shorthand; rendered to relative `.md` paths during a compile pass.

## Operations

### Ingest (when something happens worth remembering)

Trigger when:
- A phase ships (a new `cmd/agent/<feature>.go` lands)
- A forensic incident is investigated (e.g. the 2026-05-02 MBP "degraded" event)
- A perf benchmark is run
- A new machine joins the fleet
- An external dependency changes meaningfully (Nebula upstream, Tauri major bump)

Steps:
1. Drop raw artifacts into `docs/wiki/raw/<short-name>/` (logs, dumps, screenshots).
2. Read affected wiki pages.
3. Update `last_compiled` + add a 1-line entry to `log.md`.
4. Update `index.md` if a new page was added.
5. Update CLAUDE.md only when the lesson is **load-bearing across all future sessions** (e.g. a new architectural rule). Don't dump per-incident detail there — point to the wiki page.

### Query (asking the wiki a question)

1. Read `index.md` to find the relevant entity/concept/phase pages.
2. Walk backlinks from those pages.
3. Verify time-sensitive claims against current code before asserting them as fact (the `Before recommending from memory` rule from auto-memory applies here too).

### Lint (periodic maintenance, one per session at most)

Run when something feels stale. Check:

- **Orphan pages** — exist but linked from nowhere. Either delete or link from `index.md`.
- **Stale `last_compiled`** — older than 90 days for a `current` page → re-verify.
- **Contradicted claims** — page A says X, page B says NOT-X. Resolve.
- **Stale shipped claims** — page says feature Y is shipped at version Z; grep code to confirm. If absent → mark `superseded` or delete.
- **Backlinks symmetry** — if page A links to B, B should usually link back.

This is the same pattern as the v0.7.3 DPLPMTUD ghost claim that lived for months in `features.md` without code: lint catches it.

## Anti-patterns (DON'T do these)

- **Don't mirror the code.** "How does runCertRenewal work" → read renew.go. The wiki page for `concepts/cert-renewal.md` should describe the *invariants and the post-Phase-S architecture*, not the function bodies.
- **Don't write essays.** A wiki page is a glance. Long-form goes in `docs/<topic>.md` and gets backlinked.
- **Don't duplicate CLAUDE.md.** CLAUDE.md is the cross-session ledger. The wiki is the deep-dive. Each fact lives in one of them, not both.
- **Don't compile every session.** Most sessions don't generate ingest-worthy material. Forcing a compile pass on each turn → CRUFT.
- **Don't auto-edit the user's untracked files.** `q.md` and similar scratch files belong to the user; the wiki ingests by reading + summarizing, not by moving.

## Bootstrap state (2026-05-03)

This is the first compile pass. Seeded pages:
- entities: [[mbp]], [[mac-mini]], [[hopssh-cloud]]
- concepts: [[sleep-wake]], [[cert-renewal]], [[watchdog]], [[macos-system-mode]]
- phases: [[phase-s]], [[phase-t]]
- benchmarks: [[cellular-baseline]] (placeholder)

Everything else is added on demand.
