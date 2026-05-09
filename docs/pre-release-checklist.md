# hopssh — pre-release checklist

*Last updated: 2026-05-09 — current shipping version: v0.11.4*

Authoritative gate for the public 0.12.0 / 1.0 release. Items below are organized by must-have / should-have / nice-to-have. Ship when all "must-have" rows are green.

## Must-have (gates the release)

### Documentation freshness

- [x] **`docs/wiki/phases/phase-ledger.md`** — single canonical phase table — EXISTS as of Phase LL Tier 1 (commit `564465b`).
- [x] **`docs/features.md`** — current to v0.11.4 — refreshed in Phase LL Tier 1.
- [x] **`docs/roadmap.md`** — Recent Releases section + Phase 2A status — refreshed in Phase LL Tier 1.
- [x] **`CLAUDE.md` Discovery Log** — Phase EE→II.4 + KK consolidated entries present — Phase LL Tier 1.
- [ ] **`docs/architecture.md`** — needs refresh; currently 3 weeks old, predates multi-network + watchdog architecture + Tauri client — **TODO Phase LL Tier 3 follow-up.**
- [ ] **`docs/competitive-analysis.md`** — needs the "macOS desktop now ships" struck-out gap update similar to roadmap.md — **TODO.**
- [ ] **`CHANGELOG.md`** at repo root — does not exist; release notes currently live only in commit messages + the phase ledger. **Decision needed**: generate a curated CHANGELOG.md from `git log` for users + community PR review, OR rely on GitHub Releases page. Recommend: generate, since `git log --oneline | grep Phase` already gives a clean ledger.

### Code health

- [x] **All 41 cargo tripwire tests pass** (verified Phase LL Tier 2 — LL.6).
- [x] **`open_ssh_to_peer` removed** (only negative-assertion mentions remain, per Phase II.3 supersession).
- [x] **Phase II.4 OS icons + tooltips** wired across both desktop + dashboard.
- [x] **`docs/wiki/` lint clean** — `bash scripts/wiki-lint.sh --all` returns exit 0.
- [ ] **Go test suite + frontend svelte-check on a clean checkout** — known pre-existing svelte-check warnings in `network-topology.svelte` + `+page.svelte` (lines 270/580/1246) unrelated to recent ships; should be cleaned up OR explicitly waived in release notes.

### Distribution + signing

- [ ] **Apple Developer Program account** — required for proper notarization. Currently shipping ad-hoc-signed `.app` via `curl install-mac.sh` Gatekeeper bypass. Works but not the recommended path for public release.
- [ ] **Notarization pipeline** — `xcrun notarytool submit` + `xcrun stapler staple` in CI. Will resolve the Sequoia/Tahoe Gatekeeper friction on DMG path (curl-pipe path stays as the install-script alternative).
- [ ] **Cross-platform desktop builds** — currently macOS-only. Per `docs/wiki/decisions/client-windows-linux-architecture.md` the Windows + Linux desktop work is planned but not yet started. **Release decision**: ship 1.0 with macOS-only desktop and Linux/Windows in 1.1, OR delay 1.0 until cross-platform is ready. Recommend the former — Mac-only is honest about the user base and the agent already runs everywhere via CLI.
- [x] **Curl-pipeable installer** — `curl https://hopssh.com/install-mac.sh | bash` works (per `internal/api/distribution.go`).

## Should-have (highly recommended)

- [ ] **Public roadmap.md** — currently has internal pricing rationale + adoption funnel sections. Decide what's public-facing vs internal. Could split into `roadmap-public.md` + `roadmap-internal.md`.
- [ ] **Connection diagnostics, dashboard side** (Phase 2A item #4) — desktop client side ships via Phase GG; dashboard "Diagnose this connection — why is this relayed?" still TBD. ~M complexity per the roadmap; would close one of the most cited cross-competitor pain points ("Why is my connection relayed?").
- [ ] **iOS / Android client substrate** (`internal/client/` shared core + gomobile binding) — referenced extensively in `docs/wiki/decisions/client-{ios,android}-architecture.md` but NOT YET BUILT. Verified 2026-05-07: `internal/client/` directory does not exist. Mobile clients are paper-blocked on this.
- [ ] **README hardware/macOS-version requirements** — minimum macOS version for the desktop client (currently `bundle.macOS.minimumSystemVersion` per the macOS ADR; needs a public-facing line in README so users on older systems know upfront).

## Nice-to-have (post-1.0 candidates)

- [ ] **Subnet routing** (Phase 2A #5) — `unsafe_routes` Nebula support, would unlock the homelab gateway use case (#1 reason selfhosters adopt mesh VPN).
- [ ] **Exit nodes** (Phase 2A #6) — straightforward Nebula `0.0.0.0/0` config. ~3-5 days.
- [ ] **GitHub OAuth** (Phase 2A #3) — `github_id` column already in `users` table; ~200-300 LOC.
- [ ] **Bulk node operations** (Phase 2A #7) — multi-select + batch endpoints.
- [ ] **Granular firewall rules** (Phase 2B #8) — Nebula group-based firewall is supported; needs UI + tag schema.
- [ ] **Webhooks** (Phase 2B #9) — `EventHub` pub/sub already publishes 8+ event types; webhooks are an HTTP delivery layer on top.

## Pre-release verification protocol

Before tagging the release version (e.g., `v1.0.0`):

1. **Phase ledger + CLAUDE.md DL + features.md + roadmap.md** all in agreement on what's shipped — spot-check 5 random phases.
2. **Code-claim validation** — run the `cargo test --lib` in `clients/desktop/src-tauri/` and the Go test suite in `cmd/agent/` + `internal/...`. All green.
3. **Wiki lint** clean — `bash scripts/wiki-lint.sh --all` exits 0.
4. **End-to-end smoke** on a clean macOS user account:
   - `curl https://hopssh.com/install-mac.sh | bash` installs cleanly without quarantine prompts
   - First-launch onboarding completes (device-flow enrollment against hopssh.com)
   - System-mode upgrade prompt one-shots cleanly
   - Tray icon, autostart, Activity tab, Settings → About diagnostics all work
   - Terminal button on a peer opens the dashboard webview + connects + flows keystrokes
5. **Reboot test** — after first install + system-mode + autostart toggle, reboot the Mac. Verify: agent autostarts, .app autostarts (per Phase Y), peers reconnect, no stuck state.
6. **Failure-mode test** — run `sudo launchctl kickstart -k system/com.hopssh.agent`. Within ~5s the .app should detect the rotate and re-attach (Phase X). No "hopssh isn't running" stuck state.
7. **Uninstall test** — Settings → Danger zone → Uninstall. Auto-quit countdown completes, .app exits, all artifacts removed (Phase AA hygiene). Verify by checking `~/Library/LaunchAgents/com.hopssh.desktop.plist` is gone post-quit.

## Out-of-scope for the audit

The following are deliberately not gating the release; they're tracked for visibility:

- Per-phase wiki pages (`docs/wiki/phases/phase-X.md`) for every shipped phase. Only `phase-s.md` and `phase-t.md` exist as full write-ups; the rest live in the Discovery Log + the phase ledger. Adopting the convention "only major architecture-shaping phases get their own wiki page" — most phases captured by ledger row + DL paragraph is sufficient.
- Mobile client implementation (iOS/Android). Plans are detailed in the wiki; substrate not built. This is a 1.1+ release window.
- SSO/OIDC, scoped API keys, granular firewall rules. Tracked in roadmap.md Phase 2B/2C. Enterprise gates; not a 1.0 blocker.

## Backlinks

- [Phase ledger](wiki/phases/phase-ledger.md) — every shipped phase, version, commit, status
- [Roadmap](roadmap.md) — forward-looking strategy + Phase 2A/2B/2C item list
- [Features](features.md) — current shipping inventory
- [CLAUDE.md Discovery Log](../CLAUDE.md#discovery-log) — architectural lessons captured per phase
