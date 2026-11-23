---
type: runbook
title: Release v0.11.27 (server, then every device)
status: ready, not started
last_compiled: 2026-11-24
sources:
  - Makefile
  - .github/workflows/release.yml
  - .github/workflows/ci.yml
  - internal/api/distribution.go
---

# Release v0.11.27

**TL;DR.** Push `main` and wait for CI, copy the database, tag, wait for the release build, move the server to the new image, check it, then enroll every device again. Nothing in the tag's own workflows runs a test, so CI on `main` is the gate.

What is in it: [CHANGELOG](../../../CHANGELOG.md). Why: [[../incidents/2026-11-18-mbp-all-certs-expired]].

## Before

| | State on 2026-11-24 |
|---|---|
| Production deletes no more devices | done: `HOPSSH_NODE_PRUNE_AGE_HOURS=0`, log says `auto-prune disabled` |
| Data volume is not on a network file system (WAL needs a local one) | done: ext4 on a local disk |
| Database copied | done once; **copy again in step 3**, it has no devices yet |
| The fixes on `main` are public before the server has them | the repository is public: push, tag and deploy on the same day |
| Docker image built | never; step 4 is the first time |
| Windows | never run, only compiled |

## Steps

1. **Push `main`.**

   ```bash
   git switch main && git merge --ff-only release/v0.11.27 && git push origin refs/heads/main
   ```

   The full name is needed: the repository also has a **tag** called `main` (a desktop build of 2026-05-09), and `git push origin main` stops with "matches more than one".

2. **Wait for CI to be green.** First run with Go 1.26, with the E2E tests and with the kernel-TUN step. If one of them fails on the runner, stop here.

3. **Copy the database on the server** (SQLite online backup, then `PRAGMA integrity_check`). Never copy the file alone while the server runs. Keep the copy on the server: it holds keys. How, without SSH: the branch `ops/hopssh-prerelease` of the private deploy repository. Commit `f136e16` copies; it refuses to overwrite the copy of 2026-11-24, so give it a new file name. Its tip (`4f696d0`) reads the log for step 6.

4. **Tag.** On a clean `main` that equals `origin/main`; `make release` checks neither.

   ```bash
   make release
   ```

   Wait until both workflows are done: `Release` (12 binaries, `SHA256SUMS`, the image `ghcr.io/trustos/hopssh:v0.11.27` for amd64 and arm64) and `Release Desktop`. From the moment the GitHub release exists, the old server also offers v0.11.27 to agents; old server and new agent work together (`TestE2E_Upgrade`).

5. **Deploy.** Change the image tag in the Nomad job from `v0.11.26` to `v0.11.27`, leave `HOPSSH_NODE_PRUNE_AGE_HOURS=0` in it, push. The swap takes 5 to 7 minutes.

6. **Check the server.**

   ```bash
   curl -sf https://hopssh.com/version; curl -sf https://hopssh.com/healthz
   ```

   `current` must say `v0.11.27` (`version` follows GitHub and proves nothing). In the log: one `lighthouse started` per network, `auto-prune disabled`, no `database is locked`. Next to the database there must now be a `-wal` file.

7. **Every device**, because none is left on the server. Install, leave, enroll, look at it in the dashboard:

   ```bash
   curl -fsSL https://hopssh.com/install.sh | sh
   ```

   ```bash
   hop-agent leave --network <name>
   ```

   ```bash
   hop-agent enroll --endpoint https://hopssh.com
   ```

   On a Mac with the app: `curl -fsSL https://hopssh.com/install-mac.sh | bash`, then leave and join in the app. Before reinstalling the MBP, save its logs ([[../open-questions]], "logs wanted from the MBP").

8. **After 13 hours** every device must have renewed once: `cert OK` in its log with a new expiry, and it is still `online`. This is bug E; waiting a full 24 hours is the proof.

## Going back

Put the image tag back to `v0.11.26`. The database needs nothing: the old server reads a WAL file (`TestE2E_Upgrade` goes back too). Keep `HOPSSH_NODE_PRUNE_AGE_HOURS=0`; without it v0.11.26 deletes silent devices again. Agents can stay on v0.11.27.

## Afterwards

- `CHANGELOG.md`: correct the date if it was another day.
- Delete the branch `ops/hopssh-prerelease` in the deploy repository.
- Set `status` / `shipped_in` on the incident page, the ledger row and [[../concepts/e2e-tests]].
- [[../open-questions]]: the two entries this release makes checkable are "do the macOS send bugs explain earlier symptoms?" and "kernel TUN: what is still unproven".
