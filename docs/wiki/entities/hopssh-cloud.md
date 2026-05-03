---
type: entity
title: hopssh.com control plane (cloud)
status: current
last_compiled: 2026-05-03
sources:
  - CLAUDE.md (Discovery Log)
  - oci_nomad_cluster/jobs/hopssh.nomad.hcl
  - .github/workflows/release.yml
---

# hopssh.com control plane

## TL;DR

Single Go binary (`hop-server`) deployed on Oracle Cloud arm64 worker, fronted by Traefik at `hopssh.com`. Nomad-orchestrated; new images at `ghcr.io/trustos/hopssh:<tag>` are picked up by a GitHub-hosted runner that bumps the Nomad job. Lighthouse + relay UDP per-network on ports 42001–42100. Heartbeats land on TCP 9473 behind HTTPS.

## Endpoints

- **HTTPS (API + dashboard)**: `https://hopssh.com` → Traefik → container port 9473
- **Lighthouse / relay UDP**: `132.145.232.64:42001` (home), `:42002` (work). Per-network instances of vendored Nebula, each with its own CA + cert pool.
- **Version probe**: `GET https://hopssh.com/version` → `{"current":"vX.Y.Z","version":"vX.Y.Z"}` — used to verify deploys.

## Deploy flow

1. `make release` (or manual `git tag vX.Y.Z && git push origin vX.Y.Z`) — CI builds + publishes `ghcr.io/trustos/hopssh:vX.Y.Z` image.
2. Bump image tag in `oci_nomad_cluster/jobs/hopssh.nomad.hcl`, commit + push.
3. GitHub Actions runner on the OCI cluster pulls + redeploys via `nomad job run`. Container swap takes ~5–7 minutes.
4. Verify via `curl -sf https://hopssh.com/version`.

## Side effects of deploys (worth knowing)

- During the swap, the Nebula instances bounce. **Agents see `Close tunnel received, tearing down. certName=hopssh-server` for the lighthouse vpnAddrs**.
- Heartbeat 404s appear briefly mid-rollout (Traefik routes mid-update).
- **The Phase P2 [[../concepts/watchdog]] correctly fires** if a per-network keepalive sees 3 consecutive stuck cycles (~270s) of probe failures. This happened on 2026-05-02 v0.10.84 deploy → forensic dump on the [[mbp]]. Functionally correct (auto-recovered) but noisy.

## Cluster notes

- **arm64**, Nomad + Docker, distroless image.
- **No SSH access** — Oracle's NSG blocks 22 to the worker. Operate via Nomad UI / runner / `nomad alloc exec`.
- **GitHub Action runner** lives on the same VM; that's the only privileged surface. PAT in env, never in-repo.

## Backlinks

- [[mbp]], [[mac-mini]] — heartbeat / lighthouse clients
- [[../concepts/watchdog]] — interaction with deploy bounces
