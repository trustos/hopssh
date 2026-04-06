# hopssh

**Hop into your network. Your servers, your rules.**

Encrypted mesh networking with P2P, relay fallback, built-in DNS, and a web terminal.
The best self-hosted alternative to Tailscale and ZeroTier. Single binary, zero infrastructure.

Website: https://hopssh.com
Domain: hopssh.com
CLI name: `hop`

---

## Product Overview

hopssh is an encrypted mesh networking platform built on Nebula. It creates P2P tunnels
between your devices with automatic relay fallback, built-in DNS resolution, and a web-based
management dashboard — including a browser terminal for server access.

Think ZeroTier/Tailscale, but:
- **Best self-hosted experience** — single binary, embedded web UI, SQLite, zero infrastructure
- **Browser-based web terminal** — SSH into any node from the dashboard (no one else has this)
- **User-defined DNS** — `jellyfin.zero`, `nas.home`, `db.prod` — pick your own domain per network
- **Visual mesh management** — see P2P vs relayed connections, exposed services, DNS records

### What it is
- **Mesh network** — P2P encrypted tunnels between all your devices (Nebula, Noise Protocol)
- **Control plane** (hosted or self-hosted) — manages networks, nodes, PKI, DNS, lighthouse+relay
- **Agent** — installed on servers, joins the mesh, exposes services
- **Client** — installed on laptops/phones, joins the mesh, accesses services
- **Web dashboard** — manage networks, nodes, DNS, port exposure; built-in web terminal
- **Built-in DNS** — `hostname.yourdomain` resolves to mesh IPs, user-defined domains per network

### How it compares

| | ZeroTier | Tailscale | hopssh |
|---|---|---|---|
| P2P mesh | Yes | Yes | Yes (Nebula) |
| Relay fallback | Roots (UDP) | DERP (TCP/443) | Lighthouse relay (UDP) |
| Self-hosted control | Clunky | Headscale (separate) | First-class (single binary) |
| Self-hosted relay | Moons (no UI) | DERP (manual) | Built-in (dashboard config) |
| Web terminal | No | No | Yes (browser-based PTY) |
| DNS | Manual | MagicDNS | User-defined domains |
| Management UI | Hosted only | Limited | Always (embedded in binary) |
| Protocol | Custom | WireGuard | Nebula (Noise, Curve25519) |
| License | BSL | Proprietary | MIT (Nebula) |

---

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│               hopssh Control Plane (single binary)           │
│                                                              │
│  ┌──────────┐  ┌─────────────────────────────────────────┐  │
│  │ API +    │  │ Per-Network Nebula Instances             │  │
│  │ Web UI   │  │                                          │  │
│  │ :9473    │  │  Network "home" (CA-1, domain: .zero)    │  │
│  │ TCP      │  │  ├─ Lighthouse+Relay (.1) UDP :42001    │  │
│  │          │  │  └─ DNS: jellyfin.zero → 10.42.1.3      │  │
│  │          │  │                                          │  │
│  │          │  │  Network "prod" (CA-2, domain: .prod)    │  │
│  │          │  │  ├─ Lighthouse+Relay (.1) UDP :42002    │  │
│  │          │  │  └─ DNS: web.prod → 10.42.2.2           │  │
│  └──────────┘  └─────────────────────────────────────────┘  │
└──────────────────┬────────────────────┬──────────────────────┘
                   │ TCP :9473          │ UDP :42001-N
                   │ (API/Web)          │ (Nebula per network)
                   │                    │
          ┌────────┘              ┌─────┘
          │                       │
     ┌────┴────┐           ┌─────┴──────────────────┐
     │ Browser │           │ Agents & Clients        │
     │ (manage,│           │                         │
     │ terminal│           │  Agent A ←─P2P─→ Agent B│
     │  proxy) │           │     └──relay──┘         │
     └─────────┘           │  Client C (laptop)      │
                           │  Client D (phone)        │
                           └──────────────────────────┘
```

### Connection flows

**P2P (primary, ~92% of connections):**
Agent A asks lighthouse "where is B?" → lighthouse returns B's endpoint → A and B establish direct UDP tunnel via hole punching.

**Relay (fallback, ~8% — symmetric NAT, firewalls):**
Agent A → Lighthouse/Relay → Agent B. E2E encrypted, relay is blind.

**Web terminal (browser → agent through control plane):**
Browser → HTTPS to control plane → Nebula mesh → Agent. Always through control plane since browsers can't join the mesh directly.

### Trust model
- All traffic encrypted end-to-end via Nebula (Noise Protocol, Curve25519)
- Per-network CA — networks are cryptographically isolated (separate Nebula CAs)
- Nodes authenticate with per-node certificates (24h, auto-renewed)
- Agent tokens encrypted at rest (AES-256-GCM), verified with constant-time comparison
- Enrollment tokens SHA-256 hashed, single-use, 10-minute TTL
- Control plane never stores SSH keys, cloud credentials, or server passwords
- Relay is blind — cannot decrypt traffic (just forwards opaque Nebula packets)
- Firewall groups (agent/user/admin) control what each node type can access
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

## Core Features

### Phase 1 — Mesh Networking (current)
- [x] Agent enrollment (4 modes: device flow, token, bundle, IaC)
- [x] Networks (isolated mesh per network, auto PKI, per-network CA)
- [x] Web terminal (browser shell via WebSocket PTY through mesh)
- [x] Port forwarding (TCP tunnel, any port)
- [x] Node health dashboard (connected, OS, uptime)
- [x] Audit logging (login, shell, exec, port-forward)
- [x] Short-lived certificates (24h) with auto-renewal
- [ ] Persistent lighthouse + relay per network
- [ ] P2P direct connections via Nebula hole punching
- [ ] Relay fallback through lighthouse when P2P fails
- [ ] Client app (`hop client join`) for laptops/phones
- [ ] Built-in DNS with user-defined domains (`.zero`, `.prod`, `.lab`)
- [ ] Service exposure config (which ports are mesh-accessible)
- [ ] Firewall groups (agent/user/admin roles via Nebula certs)

### Phase 2 — Teams + Management
- [ ] Team invitations (email invite, instant mesh access)
- [ ] ACL rules (fine-grained access control beyond groups)
- [ ] Regional relay nodes (add relays via dashboard)
- [ ] Peer connectivity map (P2P vs relayed status)
- [ ] API keys for automation
- [ ] GitHub OAuth login
- [ ] Terraform/Pulumi provider

### Phase 3 — Enterprise + Scale
- [ ] SSO / SAML
- [ ] RBAC (granular permissions)
- [ ] Session recording
- [ ] Desktop tray app (macOS, Windows, Linux)
- [ ] Mobile clients (iOS, Android)
- [ ] Bandwidth monitoring per network

---

## Pricing Model

### Hosted (hopssh.com)
| Tier | Price | Limits |
|---|---|---|
| Free | $0 | 10 nodes, 1 network, P2P + relay |
| Pro | $5/node/month | Unlimited networks, DNS, web terminal, audit, API |
| Enterprise | Contact | SSO, RBAC, session recording, regional relays, SLA |

### Self-hosted
Free forever. Run the single binary on your own server. All features included.

---

## Project Structure

```
cmd/
  agent/              Agent + client binary (serve, enroll, client join)
  server/             Control plane server (API, lighthouse, relay, DNS)

internal/
  api/                HTTP handlers (auth, networks, nodes, proxy, device, bundles, dns, peers)
  auth/               Middleware (session auth, rate limiting)
  authz/              Authorization (CanAccessNetwork — future: teams, ACLs)
  crypto/             AES-256-GCM encryption at rest
  db/                 SQLite stores + migrations + sqlc generated code
  frontend/           Embedded SPA (built from frontend/)
  mesh/               NetworkManager (persistent per-network Nebula instances)
  pki/                Nebula CA + cert generation

frontend/             Svelte 5 SPA (shadcn-svelte, Tailwind, xterm.js)
patches/              Vendor patches (Nebula graceful shutdown)
scripts/              Maintenance scripts
docs/                 Architecture, enrollment guide, developer guide

.github/workflows/    CI (build, vet, test, cross-compile, patch check)
Dockerfile            Multi-stage build
Makefile              Build, vendor, patch, frontend, test
```

---

## Development

See [docs/development.md](docs/development.md) for the full developer guide.

```bash
# First-time setup (vendor + apply patches):
make setup

# Build everything (frontend + Go):
make build-all

# Run control plane:
./hop-server

# Enroll a server:
echo '<token>' | sudo ./hop-agent enroll --token-stdin --endpoint http://<control-plane>:9473

# Join as a client (laptop):
./hop-agent client join --network <id> --endpoint http://<control-plane>:9473

# Docker:
docker build -t hopssh .
docker run -p 9473:9473 -p 42001-42100:42001-42100/udp -v hopssh-data:/data hopssh
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
