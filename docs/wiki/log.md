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
