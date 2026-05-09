# hopssh wiki — index

> **Read [SCHEMA.md](SCHEMA.md) before maintaining or extending this wiki.** It's the operating manual. Wiki bootstrapped via the [dev-wiki framework](https://github.com/trustos/dev-wiki) (vendored — see `docs/wiki/.framework-version`).

## Page types in use

Karpathy's gist enumerates 6 canonical page types (summary, entity, concept, comparison, synthesis, overview). For a code project we use seven (4 carried + 3 dev-domain additions):

| Our type | Maps to gist type(s) | Examples |
|---|---|---|
| `entity` | entity | machines, networks, services |
| `concept` | concept, overview | architectural ideas (sleep-wake, watchdog) |
| `decision` (ADR) | (new — dev-domain) | why we chose Y over Z |
| `incident` | (new — dev-domain) | forensic post-mortems |
| `phase` | synthesis + summary, dated | per-phase post-mortems with shipped version |
| `benchmark` | comparison + summary | perf baselines, regression markers |
| `runbook` | (new — dev-domain) | symptom → action operational wisdom |

Per-source `summary` pages (Karpathy's pattern for an article-research wiki) don't fit because our raw sources are forensic dumps and code commits — synthesis happens at the incident or phase level, not per-document.

## Entities

Real-world things — machines, networks, services, accounts.

- [[entities/mbp]] — yavors-macbook-pro (M-series, lid-closed laptop, primary sleep/wake test subject)
- [[entities/mac-mini]] — tenevis-mac-mini-2 (always-on, home network, behind UPnP-capable TP-Link router with stable public IP `46.10.240.91`)
- [[entities/hopssh-cloud]] — control plane on Oracle Cloud (arm64), Nomad-deployed, Traefik-fronted, hopssh.com

## Concepts

Architectural ideas the code embodies but doesn't explain.

- [[concepts/sleep-wake]] — what survives sleep on macOS, what doesn't, why
- [[concepts/cert-renewal]] — 24h cert lifecycle, Phase S 60s ticker, retry-with-backoff
- [[concepts/watchdog]] — Phase P2 stuck-data-plane watchdog, restartFn, forensic dumps
- [[concepts/macos-system-mode]] — bundled gvisor vs LaunchDaemon kernel-utun, mirror-token + mirror-port handoff
- [[concepts/desktop-client]] — Tauri 2 + Svelte 5 macOS app: shipped capabilities (Phase V→DD), 22 commands, forward gaps
- [[concepts/client-strategy]] — unified Tauri 2 delivery across iOS/Android/macOS/Windows/Linux: shared Svelte UI + gomobile core, status table

## Decisions (ADRs)

Why we chose Y over Z. Status: proposed / accepted / superseded.

- [[decisions/phase-s-renewal-ticker]] — `time.NewTicker(60s)` over `time.After(longSleep)` for cert renewal across macOS sleep (accepted, shipped v0.10.82)
- [[decisions/client-macos-architecture]] — macOS Tauri shell + sidecar `hop-agent` (accepted, shipped Phase V→DD = v0.10.96)
- [[decisions/client-windows-linux-architecture]] — Windows + Linux desktop delta on top of macOS baseline (proposed, agent-side ready)
- [[decisions/client-ios-architecture]] — iOS Tauri main app + Network Extension + gomobile xcframework (proposed, substrate-blocked)
- [[decisions/client-android-architecture]] — Android Tauri + VpnService foreground + gomobile aar (proposed, substrate-blocked)

## Incidents

Forensic post-mortems of specific failure events.

- [[incidents/2026-05-02-mbp-watchdog-deploy-bounce]] — work-network watchdog tripped during v0.10.84 control-plane rollout; auto-recovered (resolved)
- [[incidents/2026-05-07-mbp-watcher-wedge]] — watchNetworkChanges deadlocked in vendor Nebula call; third watchdog class added in Phase DD (v0.10.96, resolved)

## Phases

Per-phase shipped state, post-mortems, what was learned.

- [[phases/phase-ledger]] — single-page canonical table mapping every phase letter → version → commit → 1-line summary → status (live / superseded). Built 2026-05-09 from `git log` + cross-referenced with CLAUDE.md Discovery Log.
- [[phases/phase-s]] — renewal loop survives macOS deep-sleep (v0.10.82, 2026-05-02)
- [[phases/phase-t]] — dashboard table column toggles + RTT replaces dead Handshake (v0.10.83 + v0.10.84, 2026-05-02 → 2026-05-03)

## Benchmarks

Empirical perf snapshots, baselines for regression comparison.

- [[benchmarks/cellular-baseline]] — Yettel BG cellular cold-start direct-P2P (placeholder; populate next session)

## Runbooks

Symptom → action operational wisdom.

- [[runbooks/mesh-dead-kickstart]] — kick the LaunchDaemon when the mesh appears dead but the agent is running
- [[runbooks/refresh-system-mode-binary]] — Phase J workaround: manually refresh `/usr/local/bin/hop-agent` from the .app

## How this maps to existing files

| Topic | Wiki page (TL;DR) | Long-form reference |
|---|---|---|
| Sleep/wake architecture | [[concepts/sleep-wake]] | `docs/sleep-wake-plan.md` |
| macOS HP screen-sharing | (skipped — too niche for wiki) | `docs/macos-hp-screen-sharing-fix.md` |
| Architecture overview | (skipped — covered by code) | `docs/architecture.md` |
| Multi-network design | (TBD) | `docs/multi-network-per-agent-plan.md` |
| Performance work | (skipped) | `docs/performance.md`, `spike/` |

The wiki doesn't replace `docs/`. Wiki pages link out to the long-form docs where they exist.

## Output formats supported

Karpathy's gist enumerates these as first-class outputs the LLM can produce on query:

- **Markdown** (default — files in this wiki, comparison tables inline)
- **Marp slide decks** (markdown → presentation; Obsidian Marp plugin renders inline)
- **matplotlib charts** (Python; data extracted from wiki tables)
- **Obsidian Canvas** (`.canvas` files; visual concept maps)

All optional. The wiki's primary surface is markdown; the rest are on-demand.

## Scaling note

The current wiki is 13 pages, ~1000 lines. The LLM can navigate this directly via `index.md` + backlinks. Karpathy's gist mentions [`qmd`](https://github.com/karpathy/qmd) as a BM25/vector hybrid local search MCP server for when the wiki grows large. **Defer until ~100 pages** — earlier than that, indexed search is overkill versus reading the index + walking links.
