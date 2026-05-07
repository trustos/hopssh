# hopssh wiki — usage playbook

> **Audience: both human and agent.** This is the "what to do today" file. For *conventions and reference*, see [SCHEMA.md](SCHEMA.md). For the *pattern's theory*, see [PATTERN.md](https://github.com/trustos/dev-wiki/blob/main/PATTERN.md). This file answers: "I'm starting a task — now what?"

## TL;DR — the 4 modes you cycle through

Every session passes through some subset of these four modes. The agent mediates each.

| Mode | When | What happens |
|---|---|---|
| **1. Consult** | At the start of any non-trivial task | Read `index.md`, walk to relevant pages |
| **2. Work** | During the task | Maintain plan-of-record; rewrite after each step |
| **3. Ingest** | When something happens worth remembering | Conversational page-diff + log entry |
| **4. Lint** | Periodically (once per session at most) | `bash scripts/wiki-lint.sh --all` |

## Mode 1 — Consult (start of task)

Before answering or implementing, the agent MUST consult the wiki when the task crosses files or implicates past work. The CLAUDE.md mandate is binding for these triggers:

| Task shape | Required wiki read |
|---|---|
| **Refactor / architectural change** | `decisions/` (ADRs) + `phases/` (shipped state). Do NOT re-litigate decisions marked `status: superseded` without explaining why the prior reasoning no longer holds. |
| **Bug investigation** | `incidents/`. If a matching symptom exists, start from its root-cause section. |
| **Recommending config for a known machine/service** | `entities/<name>.md`. Don't assume defaults. |
| **Cross-cutting question** ("how does X work overall") | `concepts/` first. |
| **Operational fix** | `runbooks/`. Use the documented action; don't reinvent. |

**Skip the consult only when:** task is single-file isolated (typo, obvious bug, isolated tweak). The wiki is overkill for routine work.

If you're unsure whether to consult, default to consulting. The cost of reading `index.md` is ~2KB; the cost of redoing ruled-out work is hours.

## Mode 2 — Work (plan-of-record discipline)

For any task expected to run >5 tool calls or session >30 min:

1. **Write a plan-of-record** at `~/.claude/plans/<slug>.md` (or your project's working-memory equivalent). Format: numbered steps, current step bolded, what's done crossed out.
2. **Rewrite the plan after each meaningful step** — decision made, hypothesis ruled out, finding, revert. Not append; rewrite. The act of rewriting re-injects current state into the most recent context window position, where the LLM pays disproportionate attention. This is the Manus pattern; it defeats lost-in-the-middle on long chains.
3. **If you revert**, write the revert AND the reason into the plan-of-record. Never silently undo.

**Watch for:** the agent stops updating the plan. Sessions drift, ground gets re-covered. Push back: *"You haven't updated the plan-of-record. Refresh it."*

## Mode 3 — Ingest (something happened worth remembering)

The framework has 6 ingest triggers. **Routine bugfixes / refactors / typos do NOT trigger ingest** — that's by design (signal-to-noise rule).

| Trigger | Where it lands |
|---|---|
| Architectural decision made (chose Y over Z) | New ADR in `decisions/` |
| Incident investigated (forensic post-mortem) | New page in `incidents/` |
| Phase / release shipped | New page in `phases/` (version-stamped) |
| Performance benchmark run | New or updated page in `benchmarks/` |
| New "if you see X, do Y" wisdom | New page in `runbooks/` |
| External dependency changes meaningfully | Update existing concept page + log entry |

When a trigger fires, the agent runs the **ingest conversation**:

1. **Discusses takeaways with the user** — confirms framing, surfaces uncertainties, agrees on scope. Ingest is a conversation, not a silent compile.
2. **Drops forensic artifacts** into `raw/<topic>/` (logs, dumps, screenshots, transcripts).
3. **Proposes a page-diff** — "I want to add page A and update pages B + C, here are the bullets per page."
4. **You ack/redirect.** Agent does NOT write before approval.
5. **Writes the pages, updates `index.md`, appends to `log.md`.**

If the agent doesn't propose ingest at session end, prompt explicitly: *"Should anything go in the wiki?"*

## Mode 4 — Lint (health check)

```bash
bash scripts/wiki-lint.sh --all
```

Runs in <2 seconds. Modes (each can be invoked alone):

| Mode | What it catches |
|---|---|
| `--drift` | Wiki references files / versions / pages that no longer exist |
| `--orphans` | Pages nothing links to |
| `--stale` | Pages with `last_compiled` >90 days AND `status: current` |
| `--duplicates` | Pages whose first paragraphs hash-overlap |
| `--missing-sources` | Pages with empty `sources:` AND no prior-knowledge marker |
| `--missing-decision-basis` | ADR pages without a populated Decision basis section |
| `--blast-radius PAGE` | Lists pages linking to a target (use before refactoring/renaming) |

**When something fires:**

- **Drift** → fix the path, or mark the page `superseded`.
- **Orphans** → add a link from `index.md`, or delete the page.
- **Stale** → re-verify against current code; bump `last_compiled` or supersede.
- **Duplicates** → merge or distinguish.
- **Missing-sources** → populate `sources:` frontmatter, or add the prior-knowledge marker.
- **Missing-decision-basis** → back-fill the ADR's Decision basis section (see Mode 5).

If you can't fix immediately, file the gap to `open-questions.md`.

## Mode 5 — Decision basis (when filing an ADR)

**Every ADR MUST have a populated `## Decision basis` section.** Lint enforces this structurally; lint does NOT enforce content quality.

The Decision basis section is where the agent honestly accounts for *what produced the decision*:

```markdown
## Decision basis

The reasoning that produced this decision came from:
- **Code I read:** path/to/relevant.go (lines 42-78), path/to/test.go
- **Wiki pages I consulted:** [[concepts/related-concept]], [[phases/phase-q]]
- **External sources I fetched:** https://example.com/issue/1234 (cached at raw/topic/issue-1234.md, 2026-05-04)
- **Prior-knowledge claims (mark with confidence):**
  - [HIGH] X is the standard pattern in Go for this scenario — verified against stdlib usage.
  - [LOW] Y might also work — recalled from training data, not verified.
```

Confidence levels: `HIGH` (verified against the codebase or canonical docs in this session), `MEDIUM` (general consensus + indirect verification), `LOW` (uncertain prior-knowledge that ought to be checked before relying on).

**Why this matters:** the next session reading this ADR can trace EXACTLY what evidence backed the decision. If the codebase has shifted, the agent can re-verify the HIGH-confidence claims; LOW-confidence claims are flagged for skepticism.

## Optional: Tier R2 thought-log (opt-in discipline)

Per-workstream sister file at `~/.claude/plans/<slug>.thoughts.md`. Agent appends a 1-3 line entry whenever it makes a non-obvious decision DURING the work (not after, like an ADR — but live, as it happens):

```
## 2026-05-04T14:22:31 — chose ticker over After
- recalled: CLAUDE.md macOS sleep section (prior session)
- ruled out: time.AfterFunc (same monotonic-clock issue)
- evidence: cmd/agent/renew.go:142 (existing usage pattern)
```

Survives compaction. Reviewable by the human in real time. **NOT enforced** — opt-in by adding to your project's CLAUDE.md if you want it. Lighter than an ADR, more durable than chat history.

## Quick reference card

| Situation | Action |
|---|---|
| Starting a refactor | "Read decisions/ + phases/ first" |
| Investigating a bug | "Read incidents/ first" |
| Asking about a machine/service | "Read entities/<name>" |
| Long task running | Agent rewrites plan-of-record after each step |
| Session ending | "Anything worth filing to the wiki?" |
| Reverting work | File ADR with `status: superseded` |
| Found a useful URL | Drop into `raw/<topic>/`; agent caches excerpt |
| Tool output >2KB you'll reference later | Save to `raw/<topic>/`, reference by path |
| Want a health check | `bash scripts/wiki-lint.sh --all` |
| Want to know who links to a page | `bash scripts/wiki-lint.sh --blast-radius PAGE` |
| Cross-cutting unresolved item | File to `docs/wiki/open-questions.md` |
| Filed an ADR | Populate `## Decision basis` (otherwise lint fails) |

## Concrete walkthrough — cert-renewal investigation (with vs without the wiki)

**Scenario:** *"The cert renewal seems flaky again on the laptop after sleep."*

**Without the wiki (cold start):**
1. Agent searches the codebase, re-derives that there was a Phase S fix.
2. Maybe finds it; maybe not.
3. Investigates from scratch.
4. Possibly proposes a fix that re-introduces what Phase S solved.

Time: 30+ minutes; risk of regression.

**With the wiki (operating contract followed):**
1. Agent's CLAUDE.md says: bug investigation → search `incidents/` first.
2. Agent reads `index.md`, sees `[[incidents/2026-05-02-mbp-watchdog-deploy-bounce]]` and `[[decisions/phase-s-renewal-ticker]]`.
3. Reads both pages — knows Phase S was the `time.NewTicker(60s)` fix, knows the deploy-bounce incident was a different root cause, knows monotonic-clock-anchored sleep is a load-bearing rule.
4. Investigates with that context. Proposes a fix that doesn't regress Phase S, OR identifies that this is the same incident shape and links existing pages instead of writing new ones.

Time: <5 minutes; no regression risk.

The wiki turned a 30-minute "rediscover what we already know" investigation into a 30-second "read the relevant pages" lookup.

## Watch for these failure modes

The framework reduces, doesn't eliminate. Watch for:

- **Agent skips the wiki anyway** → especially on tasks that *seem* small but cross files. Push back: *"Did you read `index.md`?"*
- **Wiki page goes stale** → lint catches mechanical drift; the human catches semantic drift (page says X works a certain way, architecture changed). Periodic skim helps.
- **Ingest skipped on a real trigger** → end of long session, agent forgets. Habit-build by asking explicitly.
- **Pages bloat** → wiki page should fit one screen. If it grows past that, split (concept page + linked sub-pages).
- **`sources:` empty** → lint will catch with `--missing-sources`. Either populate or add the prior-knowledge marker.
- **Decision basis perfunctory** → "code: [paths]" left as a literal placeholder. Lint can't catch low-quality fills; humans must.

## What's NOT in scope of the wiki

So you don't waste effort:

- **Don't mirror the code.** "How does X work" → read the code. Wiki is for *why*, *what we learned*, *what crosses files*.
- **Don't replace tests.** Tests stay in `*_test.go`.
- **Don't replace CLAUDE.md.** CLAUDE.md is auto-loaded rules; wiki is opt-in deep-dive.
- **Don't replace `~/.claude/plans/`.** That's working memory for the active session.
- **Don't compile every session.** Most don't generate ingest-worthy material.
- **Don't enforce in-prose citation via lint.** Lint only enforces structural fields (`sources:`, `## Decision basis`). Content quality is a human review concern.

---

**The wiki is a capability, not a process.** Activate it when the task warrants it; skip it when it doesn't. The framework's job is to make activation cheap. Your (human + agent) job is the reflex of asking *"should we consult first?"* and *"should we ingest after?"* — and to let the agent do the bookkeeping.
