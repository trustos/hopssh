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
- Agent authenticates to control plane with per-node enrollment token (SHA-256 hashed at rest)
- Agent tokens encrypted at rest (AES-256-GCM), verified with constant-time comparison
- Control plane authenticates users via browser session (Secure cookie) or API key
- All traffic encrypted end-to-end via Nebula (Noise Protocol, Curve25519)
- Per-network CA — networks are cryptographically isolated
- Control plane never stores SSH keys, cloud credentials, or server passwords
- If agent stops, control plane loses access — customer always in control
- Agent uploads restricted to `/var/hop-agent/uploads/` (no arbitrary file writes)

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
| Mesh | Nebula (userspace, gvisor), Curve25519 PKI — see Fork Notes below |
| Frontend | Svelte 5 SPA, embedded in Go binary |
| Auth | Session-based + API keys |

---

## Vendor Patch: Nebula

We vendor dependencies and apply a 1-line patch to `slackhq/nebula` to fix a critical bug:
upstream `svc.Close()` calls `os.Exit(2)` which kills the entire control plane process.
The root cause is in `interface.go` — the error check matches `os.ErrClosed` but userspace
mode returns `io.ErrClosedPipe`. Our patch adds the missing error check.

- **Upstream issue**: https://github.com/slackhq/nebula/issues/1031
- **Upstream fix PR**: https://github.com/slackhq/nebula/pull/1375 (open, not merged)
- **Our patch**: `patches/nebula-1031-graceful-shutdown.patch` (1 line in `interface.go`)
- **Apply**: `make patch-vendor` (or `make vendor` which vendors + patches)
- **Check script**: `scripts/check-nebula-patch.sh` — run monthly or in CI
- **Build**: use `go build -mod=vendor ./...` (or `make build`)
- **When to drop**: When PR #1375 merges and a new nebula release includes the fix,
  update the version, re-vendor, and remove the patch file

---

## Core Features (MVP)

### Tier 1 — Launch
- [x] Agent enrollment (4 modes: device flow, token, bundle, IaC)
- [x] Web terminal (browser shell via WebSocket PTY through mesh)
- [x] Port forwarding (TCP tunnel, any port)
- [x] Node health dashboard (connected, OS, uptime)
- [x] Networks (isolated mesh per project, auto PKI)
- [x] Audit logging (login, shell, exec, port-forward)
- [ ] GitHub OAuth login
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

## Project Structure

```
cmd/
  agent/              Agent binary (serve + enroll subcommands)
  server/             Control plane server

internal/
  api/                HTTP handlers (auth, networks, nodes, proxy, device flow, bundles)
  auth/               Middleware (session auth, rate limiting)
  crypto/             AES-256-GCM encryption at rest
  db/                 SQLite stores + migrations
  mesh/               Nebula tunnel manager (patched — see Vendor Patch section)
  pki/                Nebula CA + cert generation

patches/              Vendor patches for dependencies
scripts/              Maintenance scripts (patch checking)
docs/                 Architecture, enrollment guide, developer guide
frontend/             Svelte 5 SPA (pending)

.github/workflows/    CI (build, vet, test, cross-compile, patch check)
Dockerfile            Multi-stage build for containerized deployment
Makefile              Build, vendor, patch, test targets
```

---

## Development

See [docs/development.md](docs/development.md) for the full developer guide.

```bash
# First-time setup (vendor + apply patches):
make setup

# Build:
make build

# Run control plane:
./hop-server --addr :8080 --data ./data --endpoint http://localhost:8080

# Enroll an agent (interactive device flow):
./hop-agent enroll --endpoint http://localhost:8080

# Run agent (after enrollment):
./hop-agent serve --listen :41820 --token-file /etc/hop-agent/token

# Docker:
docker build -t hopssh .
docker run -p 8080:8080 -v hopssh-data:/data hopssh
```

---

## Coding Principles

These rules are derived from 4 rounds of security review. Follow them for all new code.

### Secrets & Credentials

- **Never store secrets in plaintext.** Encrypt at rest (AES-256-GCM via `crypto.Encryptor`) or hash (SHA-256) depending on whether the value needs to be recovered.
  - Recoverable secrets (agent tokens, keys): encrypt with `Encryptor`
  - Compare-only secrets (session tokens, enrollment tokens, device codes): store SHA-256 hash
- **Tokens must be single-use and time-bounded.** Enrollment tokens get a 10-minute TTL. Device codes get 10 minutes. Bundle URLs get 15 minutes.
- **Consume tokens atomically.** Use a database transaction with `UPDATE ... WHERE token = ? AND status = 'pending'` + check `RowsAffected`. Never SELECT then UPDATE in separate operations — that's a TOCTOU race.
- **Use `crypto/subtle.ConstantTimeCompare`** for any token comparison in the agent. Never use `==` or `!=` for bearer tokens.
- **Never pass secrets as command-line arguments** when avoidable. Prefer `--token-stdin` (read from stdin) or `--token-file` (read from file). If `--token` flag is offered, document that it's visible in `ps`.

### API Handlers

- **Always call `http.MaxBytesReader`** on `r.Body` before decoding JSON. Default: `1 << 20` (1 MB). Uploads: explicit limit.
- **Always validate ownership** before operating on a resource. Every handler touching a network must check `network.UserID != user.ID`. Every handler touching a node must go through `requireNode()` which validates network→node chain.
- **Never serialize `*db.Node` or `*db.User` directly to JSON.** Always map to a response DTO (`NodeResponse`, `UserProfile`, etc.) to prevent leaking sensitive fields.
- **Use `writeJSON(w, v)` for 200 responses** and `writeJSONStatus(w, status, v)` for non-200 (e.g., 201 Created). Never call `w.WriteHeader()` then `writeJSON()` — the Content-Type header will be silently dropped.
- **Return `409 Conflict` on UNIQUE constraint violations**, not 500. Check with `db.IsUniqueViolation(err)`.
- **Audit security-significant actions.** Login, registration, shell connect, exec, port-forward start, node delete. Use `h.audit(userID, action, &networkID, &nodeID, details)`.
- **Rate-limit all public endpoints.** Use `auth.NewRateLimiter()` middleware. Pass `trustedProxy` to control `X-Forwarded-For` trust.
- **Apply `writeTimeout` to all non-streaming routes.** Shell and exec are streaming (no timeout). Everything else gets `http.TimeoutHandler(30s)`.

### Database

- **All migrations run in transactions.** `tx.Begin()` → execute SQL → record in `schema_migrations` → `tx.Commit()`. Rollback on any error.
- **Check `rows.Err()`** after every `for rows.Next()` loop.
- **Use `wdb` (write DB) for any operation that needs atomicity** — even reads that must be serialized with a subsequent write. The `rdb` (read DB) can return stale data.
- **Allocation queries (subnet, node IP) use MAX, not COUNT.** COUNT breaks when rows are deleted. MAX ensures monotonically increasing IDs. The UNIQUE constraint is the safety net.
- **Hash before storing, hash before querying.** If a token is hashed at rest (enrollment tokens, session tokens, device codes), the lookup query must hash the input before the WHERE clause.

### Agent

- **Never interpolate user input into shell commands.** Use `exec.Command("binary", "arg1", "arg2")` directly. Never use `fmt.Sprintf("cmd %s", input)` with `sh -c`.
- **Restrict file write paths.** Uploads go to `/var/hop-agent/uploads/` only. Validate with `filepath.Clean` + `strings.HasPrefix`.
- **Set `ReadHeaderTimeout`** on all HTTP servers (10s). Prevents Slowloris attacks.
- **Add a timeout on `cmd.Wait()`** in shell cleanup (5s). Prevents blocking on zombie processes.

### Networking & TLS

- **Session cookies must set `Secure` when behind HTTPS.** Use `r.TLS != nil || (TrustedProxy && X-Forwarded-Proto == "https")`.
- **Only trust `X-Forwarded-For` / `X-Forwarded-Proto` when `--trusted-proxy` is set.** These headers are trivially spoofable by direct clients.
- **Validate WebSocket Origin.** Default to same-origin check (`origin == "http(s)://" + host`). Allow explicit origins via `AllowedOrigins` config.
- **Never serve private key material over plain HTTP.** Bundle downloads must require HTTPS.
- **Set CORS headers** via middleware. Default: same-origin only. Configurable via `--allowed-origins`.

### Go Patterns

- **Panic on `crypto/rand.Read` failure.** If the system entropy source is broken, nothing is safe. All `rand.Read` calls must check the error.
- **Use `*int` / `*string` for optional DB fields** that can be NULL. Scan into pointer types.
- **Exported functions return errors; unexported helpers can panic** on truly impossible conditions (crypto/rand failure).
- **Port forward IDs and similar user-facing identifiers must be crypto-random**, not sequential. Sequential IDs are guessable.
- **Connection relays (io.Copy bidirectional) must use half-close.** After one direction's `io.Copy` returns, call `CloseWrite()` on the other connection so the reverse copy gets EOF.
- **Context values must never carry sensitive data.** Use `UserProfile` (no password hash) in request context, not `User`.
- **SQLite read pool: 20 connections, 5 idle.** Write pool: 1 connection. WAL mode. This is the right balance for an embedded database.
