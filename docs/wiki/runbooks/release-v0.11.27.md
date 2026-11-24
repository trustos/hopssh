---
type: runbook
title: Release v0.11.27 (server, then every device)
status: steps 1 to 6 done on 2026-11-24 (the server runs v0.11.27); 7 and 8 open
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
| Database copied | done twice (step 3) |
| The fixes on `main` are public before the server has them | the repository is public: push, tag and deploy on the same day |
| Docker image built | in step 4, for the first time |
| Windows | never run, only compiled |

## Steps

1. **Push `main`.** Done 2026-11-24, `ad9bf66`.

   ```bash
   git switch main && git merge --ff-only release/v0.11.27 && git push origin refs/heads/main
   ```

   The full name is needed: the repository also has a **tag** called `main` (a desktop build of 2026-05-09), and `git push origin main` stops with "matches more than one".

2. **Wait for CI to be green.** Done: run 45336974587, 9 minutes, Go 1.26.11. First run with Go 1.26, with the E2E tests and with the kernel-TUN step. If one of them fails on the runner, stop here.

3. **Copy the database on the server.** Done 07:20 UTC: `hopssh-before-v0.11.27-release.db` (`integrity_check: ok`, no device, no session). (SQLite online backup, then `PRAGMA integrity_check`). Never copy the file alone while the server runs. Keep the copy on the server: it holds keys. How, without SSH: the branch `ops/hopssh-prerelease` of the private deploy repository. Commit `8d6680f` copies; it refuses to overwrite a copy, so give it a new file name. Its tip (`12f5177`) reads image, settings and log for step 6, `0da5cd9` also the data directory.

4. **Tag.** Done 07:21 UTC; `Release` took 3 minutes (run 45347355288). Checked: the 12 checksums equal the uploaded files; the image has amd64 and arm64 (`sha256:e6bc9540…`); Go 1.26.11 everywhere; the only patches refused are 12 and, with GNU patch, hunk 3 of 19. The agent is 24 MB instead of 20 because of Go 1.26: v0.11.26's code built with a new Go is as large. On a clean `main` that equals `origin/main`; `make release` checks neither.

   ```bash
   make release
   ```

   Wait until both workflows are done: `Release` (12 binaries, `SHA256SUMS`, the image `ghcr.io/trustos/hopssh:v0.11.27` for amd64 and arm64) and `Release Desktop`. From the moment the GitHub release exists, the old server also offers v0.11.27 to agents; old server and new agent work together (`TestE2E_Upgrade`).

5. **Deploy.** Done 07:26 UTC (`bc6bb98` in the deploy repository); the swap took under a minute. Change the image tag in the Nomad job from `v0.11.26` to `v0.11.27`, leave `HOPSSH_NODE_PRUNE_AGE_HOURS=0` in it, push. Allow 5 to 7 minutes.

6. **Check the server.** Done: `current` v0.11.27, 5 of 5 lighthouses, WAL, no warning or error in the log, 12 renewals with a wrong token within seconds all got 401 (none 429), the login page renders.

   ```bash
   curl -sf https://hopssh.com/version; curl -sf https://hopssh.com/healthz
   ```

   `current` must say `v0.11.27` (`version` follows GitHub and proves nothing). In the log: one `lighthouse started` per network, no `[sweep] deleting nodes` (v0.11.27 says nothing while pruning is off; `auto-prune disabled` was v0.11.26's line), no `database is locked`. Next to the database there must now be a `-wal` file.

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
