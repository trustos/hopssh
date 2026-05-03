# hopssh wiki — schema & operating rules

**Adapted from Karpathy's "compile-raw-into-wiki" knowledge-base architecture (X post 2026-04-03 + [GitHub gist 442a6bf](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f)).** This file is the schema: the instruction set for any LLM agent or human that maintains the wiki. Read this before ingesting new material or running a lint pass.

**Living document.** This schema co-evolves. When a convention turns out wrong (page conventions cause friction, an operation misses a use case, a tool changes), edit this file directly and append a one-line entry to `log.md` under the `schema` operation. Never argue with the schema; fix it.

**The labor split** (paraphrased from the gist): "The human's job is to curate sources, direct the analysis, ask good questions, and think about what it all means. The LLM's job is everything else." Concretely for hopssh: you (Yavor) drop raw artifacts in `raw/`, ask questions, and decide what's worth synthesizing. I (Claude) compile, link, lint, and surface contradictions. I don't decide what matters; I organize what you flag.

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
│   ├── assets/       downloaded images, screenshots (binary, kept alongside text)
│   └── <topic>/      per-incident raw artifacts (logs, dumps, transcripts)
├── entities/         machines, networks, accounts, services
├── concepts/         architectural concepts (sleep-wake, watchdog, cert-renewal)
├── phases/           per-phase post-mortems / shipped state
└── benchmarks/       perf baselines (cellular cold-start, WiFi LAN throughput)
```

`raw/` is for forensic inputs the LLM should read but never overwrite (e.g. an agent log excerpt, a `pmset -g log` dump, a screenshot transcription, a curl probe). The `raw/assets/` carve-out matches Karpathy's gist convention so the Obsidian Web Clipper (or any equivalent screenshot-grabber) has a known home for binaries that doesn't pollute per-topic folders. Other top-level dirs hold compiled markdown.

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

**Images.** Reference images via standard Markdown `![alt](../raw/assets/path.png)`. Note that LLMs **can't read inline images in the same pass as the surrounding text** — when an agent queries a page with images, expect it to read the markdown first, then view referenced images separately. Don't rely on visual layout in image alt-text alone; describe critical visual content in prose alongside.

**Log entries.** Every entry to `log.md` MUST start with `## [YYYY-MM-DD] <operation> | <short title>` so Unix tools can filter by month or operation: `grep '^## \[2026-05' log.md` returns all May entries; `grep 'lint |' log.md` returns all lint passes. Operations: `bootstrap`, `ingest`, `query`, `lint`, `schema`. After the heading, one paragraph of context, then a one-line `Pages touched:` tally.

## Operations

### Ingest (when something happens worth remembering)

**Ingest is a conversation, not a silent compile.** When you give the LLM a new source, it must:

1. Read the source.
2. **Discuss key takeaways with you** before writing — confirm framing, surface uncertainties, agree on scope. The user can redirect (correct misreadings, narrow scope, flag what's already covered) before any pages change.
3. Drop raw artifacts into `docs/wiki/raw/<short-name>/` (logs, dumps, screenshots).
4. Touch 5–15 affected entity/concept/phase pages: write a new summary, update existing pages, add cross-references, refresh `last_compiled`.
5. Update `index.md` if a new page was added.
6. Append an entry to `log.md` in the `## [YYYY-MM-DD] ingest | <title>` format.
7. Update `CLAUDE.md` only when the lesson is **load-bearing across all future sessions** (e.g. a new architectural rule). Don't dump per-incident detail there — point to the wiki page.

Trigger when:
- A phase ships (a new `cmd/agent/<feature>.go` lands)
- A forensic incident is investigated (e.g. the 2026-05-02 MBP "degraded" event)
- A perf benchmark is run
- A new machine joins the fleet
- An external dependency changes meaningfully (Nebula upstream, Tauri major bump)

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
- **Data gaps fillable by web search** — the wiki references a fact, dependency, or external claim (Tailscale issue link, vendor PR status, RFC citation) that isn't substantiated by any source page. Flag and (with user approval) run a targeted `WebFetch`, then file the result as a new wiki page or update an existing source. From the gist verbatim.

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
