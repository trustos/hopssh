# hopssh — Agent Reference

**Hop into your servers. No SSH keys needed.**

Browser-based server access through auto-provisioned encrypted mesh tunnels.
Users install a one-line agent, get instant web terminal + port forwarding + team sharing.

Website: https://hopssh.com
Domain: hopssh.com
CLI name: `hop`

---

## Product Overview

hopssh replaces SSH key management, bastion hosts, and VPN configuration with a single agent
that creates encrypted mesh tunnels back to a control plane. Users access servers through
the browser — web terminal, port forwarding, service status — with team sharing and audit logs.

### What it is
- **Control plane** (hosted or self-hosted) — manages networks, nodes, users, PKI, audit
- **Agent binary** — installed on servers, creates outbound Nebula mesh tunnel
- **Web dashboard** — browser terminal, port forwarding, node status, team management
- **Terraform/Pulumi provider** — auto-bootstrap agent on new infrastructure
- **CLI** (`hop`) — enroll existing servers, manage networks

### What it is NOT
- Not a VPN (no network-level routing between nodes)
- Not an SSH replacement library (users don't SSH — they use the browser)
- Not a configuration management tool
- Not storing SSH keys, cloud credentials, or any customer secrets

---

## Architecture

```
User's Browser
  └─ hopssh.com dashboard (or self-hosted)
       └─ Control Plane API
            ├─ Auth (session / API key)
            ├─ Network management (PKI per network)
            ├─ Node registry (health, status, audit)
            └─ Mesh Manager (userspace Nebula tunnels, on-demand)
                 └─ Agent (on customer's server)
                      ├─ Nebula tunnel (outbound UDP, NAT-friendly)
                      ├─ Web terminal (WebSocket PTY)
                      ├─ Port forwarding (TCP relay)
                      └─ Health + service status

No inbound ports required on customer servers.
Agent initiates outbound connection. Customer controls the agent lifecycle.
Control plane never holds credentials that work without agent cooperation.
```

### Trust model
- Agent authenticates to control plane with per-node enrollment token
- Control plane authenticates users via browser session or API key
- All traffic encrypted end-to-end via Nebula (Noise Protocol, Curve25519)
- Per-network CA — networks are cryptographically isolated
- Control plane never stores SSH keys, cloud credentials, or server passwords
- If agent stops, control plane loses access — customer always in control

---

## Origin

Core mesh/agent code extracted from [pulumi-ui](https://github.com/trustos/pulumi-ui),
a self-hosted Pulumi infrastructure UI. The following packages are the foundation:

| pulumi-ui package | hopssh equivalent | What it does |
|---|---|---|
| `cmd/agent/` | `cmd/agent/` | Agent binary (health, exec, shell WebSocket, PTY) |
| `internal/mesh/mesh.go` | `internal/mesh/` | Userspace Nebula tunnel manager (on-demand, idle reaper) |
| `internal/mesh/forward.go` | `internal/mesh/` | kubectl-style TCP port forwarding |
| `internal/nebula/pki.go` | `internal/pki/` | CA generation, cert issuance (Curve25519) |
| `internal/api/agent_proxy.go` | `internal/api/` | Health, shell, port forward proxy endpoints |
| `internal/agentinject/bootstrap.go` | `cmd/hop/` + install script | Agent bootstrap (adapted for one-liner install) |
| `internal/crypto/crypto.go` | `internal/crypto/` | AES-256-GCM encryption at rest |

Packages NOT carried over (Pulumi/OCI-specific):
- `internal/engine/` — Pulumi Automation API
- `internal/blueprints/` — YAML blueprint system
- `internal/applications/` — Nomad app deployment
- `internal/oci/` — OCI REST client
- `internal/agentinject/yaml.go` — Pulumi YAML injection

---

## Tech Stack

| Layer | Technology |
|---|---|
| Backend | Go, single binary, `net/http` + chi router |
| Database | SQLite (dual read/write pool, WAL mode) |
| Encryption | AES-256-GCM at rest, Nebula/Noise in transit |
| Mesh | Nebula (userspace, gvisor), Curve25519 PKI |
| Frontend | Svelte 5 SPA, embedded in Go binary |
| Auth | Session-based + API keys |

---

## Core Features (MVP)

### Tier 1 — Launch
- [ ] One-liner agent install (`curl | bash` with enrollment token)
- [ ] Web terminal (browser shell via WebSocket PTY through mesh)
- [ ] Port forwarding (TCP tunnel, any port)
- [ ] Node health dashboard (connected, OS, uptime)
- [ ] GitHub OAuth login
- [ ] Networks (isolated mesh per project, auto PKI)
- [ ] Free tier: 5 nodes, 1 user, 1 network

### Tier 2 — Team
- [ ] Team invitations (email invite, instant access)
- [ ] Audit log (who connected to what, when)
- [ ] Access revocation (remove user → instant cutoff)
- [ ] Multiple networks
- [ ] API keys for automation

### Tier 3 — Enterprise
- [ ] SSO / SAML
- [ ] RBAC (admin, operator, viewer)
- [ ] Session recording
- [ ] Self-hosted option
- [ ] Terraform provider (`meshaccess_network` resource)
- [ ] Pulumi provider (bridged from Terraform)

---

## Pricing Model

| Tier | Price | Limits |
|---|---|---|
| Free | $0 | 5 nodes, 1 user, 1 network, 24h audit |
| Team | $5/node/month | Unlimited users/networks, 90-day audit, API |
| Enterprise | $15/node/month | SSO, RBAC, session recording, self-hosted, SLA |

---

## Project Structure (planned)

```
cmd/
  hop/              CLI tool (enroll, status, networks)
  agent/            Agent binary (from pulumi-ui, stripped)
  server/           Control plane server

internal/
  api/              HTTP handlers (auth, networks, nodes, proxy)
  auth/             Session + API key middleware
  db/               SQLite stores (users, networks, nodes, audit)
  mesh/             Nebula tunnel manager (from pulumi-ui)
  pki/              CA + cert generation (from pulumi-ui)
  crypto/           AES-256-GCM encryption (from pulumi-ui)

frontend/           Svelte 5 SPA (dashboard, terminal, settings)

install.sh          One-liner agent install script (hosted at hopssh.com/install)
```

---

## Development

```bash
# Control plane
go run ./cmd/server

# Frontend dev
cd frontend && npm run dev

# Agent (for testing)
go run ./cmd/agent --token <test-token>

# CLI
go run ./cmd/hop enroll --token <token> --endpoint https://localhost:8080
```
