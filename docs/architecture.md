# hopssh — Architecture

**Status:** Backend MVP implemented (2026-04-06). Frontend pending.

---

## System Overview

```
┌─────────────────────────────────────────────────────────────┐
│                    User's Browser                            │
│  Dashboard: networks, nodes, terminal, port forwards, audit  │
└──────────────────────┬──────────────────────────────────────┘
                       │ HTTPS
┌──────────────────────▼──────────────────────────────────────┐
│                  Control Plane (cmd/server)                   │
│                                                              │
│  ┌─────────┐  ┌──────────┐  ┌─────────┐  ┌──────────────┐  │
│  │  Auth    │  │ Network  │  │  Node   │  │    Mesh      │  │
│  │ Session  │  │  CRUD    │  │ Enroll  │  │   Manager    │  │
│  │ bcrypt   │  │  PKI     │  │ Health  │  │  (Nebula)    │  │
│  │ Cookie   │  │  Subnet  │  │ Proxy   │  │  On-demand   │  │
│  └─────────┘  └──────────┘  └─────────┘  └──────┬───────┘  │
│                                                   │          │
│  ┌─────────────────────────────────────────────┐  │          │
│  │  SQLite (dual r/w pool, WAL, AES-GCM)       │  │          │
│  └─────────────────────────────────────────────┘  │          │
└───────────────────────────────────────────────────┼──────────┘
                                                    │
                              Nebula tunnel (Noise Protocol, UDP)
                              Outbound from agent, NAT-friendly
                                                    │
┌───────────────────────────────────────────────────▼──────────┐
│                Customer's Server                              │
│                                                               │
│  ┌──────────────────────────────────────────────────────┐    │
│  │  hop-agent (cmd/agent)                                │    │
│  │                                                       │    │
│  │  ├─ /health   → hostname, OS, arch, uptime (JSON)     │    │
│  │  ├─ /exec     → command execution, streaming output   │    │
│  │  ├─ /upload   → file upload to /var/hop-agent/uploads  │    │
│  │  └─ /shell    → WebSocket PTY (bash, xterm-256color)  │    │
│  │                                                       │    │
│  │  Auth: per-node Bearer token                          │    │
│  │  Ports: none inbound (outbound UDP 41820 only)        │    │
│  │  Deps: creack/pty + gorilla/websocket (no CGO)        │    │
│  └──────────────────────────────────────────────────────┘    │
│                                                               │
│  ┌──────────────────────────────────────────────────────┐    │
│  │  Nebula (separate systemd service)                    │    │
│  │  ├─ UDP 41820 outbound → control plane                │    │
│  │  ├─ nebula1 TUN interface                             │    │
│  │  └─ Firewall: TCP any from group "server"             │    │
│  └──────────────────────────────────────────────────────┘    │
└──────────────────────────────────────────────────────────────┘
```

---

## Repo Map (implemented)

```
cmd/
  agent/main.go          Agent binary: serve + enroll subcommands
  agent/enroll.go        Enrollment: device flow, token, bundle modes
  server/main.go         Control plane: wires DB, mesh, handlers, graceful shutdown

internal/
  api/
    router.go            Chi router: public + auth-protected route groups
    auth.go              Register (email validation, 8-72 char pw), login, logout, me, status
    device.go            Device authorization flow (RFC 8628) endpoints
    bundles.go           Pre-bundled tarball generation + download
    networks.go          Network CRUD: create (auto PKI + subnet), list, get, delete
    enroll.go            Node enrollment: create token, agent enroll endpoint
    proxy.go             Agent proxy: health, shell (WS relay), exec (stream), port forwards
  auth/
    middleware.go         RequireAuth middleware (session token from header or cookie)
  crypto/
    crypto.go            AES-256-GCM encrypt/decrypt (from pulumi-ui)
  db/
    db.go                SQLite DBPair (120 read + 1 write), WAL, pragma tuning
    users.go             UserStore: create, get by ID/email, count
    sessions.go          SessionStore: create, get user ID, delete, cleanup expired
    networks.go          NetworkStore: CRUD, subnet allocation, encrypted CA/server keys
    nodes.go             NodeStore: CRUD, atomic enrollment (hashed tokens, TTL), encrypted agent tokens
    device_codes.go      DeviceCodeStore: create, poll, authorize, complete
    bundles.go           BundleStore: create, claim (single-use), expire
    audit.go             AuditStore: log entries, list for network
    migrations/
      001_initial.sql    8 tables, 13 indexes (transactional migrations)
  mesh/
    mesh.go              Nebula tunnel manager: per-node tunnels, retry, idle reaper (from pulumi-ui, adapted)
    forward.go           TCP port forwarding: local listener → mesh tunnel → remote port (half-close)
  pki/
    pki.go               Nebula CA generation + cert issuance (Curve25519/ed25519)
    subnet.go            Subnet IP allocation helpers

install.sh               One-liner agent installer (curl | bash)

docs/
  architecture.md        This file
  enrollment.md          Enrollment guide (4 modes + examples)
  research.md            Market analysis + competitive landscape
  roadmap.md             5-phase development plan
```

---

## Data Model (implemented)

```sql
users (id, email, name, password_hash, github_id, created_at)
  └─ auth: email/password (bcrypt), GitHub OAuth (future)

sessions (token, user_id, created_at, expires_at)
  └─ 30-day TTL, auto-cleanup via background goroutine

api_keys (id, user_id, name, key_hash, last_used_at, created_at)
  └─ for CLI + Terraform provider (future)

networks (id, user_id, name, slug, nebula_ca_cert, nebula_ca_key[enc],
          nebula_subnet, server_cert, server_key[enc], created_at)
  └─ per-network Nebula CA (Curve25519), auto-allocated /24 subnet
  └─ server cert = control plane's identity in this network (.1 IP)
  └─ CA key + server key AES-GCM encrypted at rest

nodes (id, network_id, hostname, os, arch, nebula_cert, nebula_key[enc],
       nebula_ip, agent_token[enc], enrollment_token[hash], agent_real_ip,
       status, last_seen_at, created_at)
  └─ enrollment_token: SHA-256 hashed at rest, one-time, consumed atomically
  └─ enrollment_expires_at: 10-minute TTL from creation
  └─ agent_token: per-node Bearer token, AES-GCM encrypted at rest
  └─ status: pending → online → offline
  └─ nebula_key AES-GCM encrypted at rest

device_codes (device_code, user_code, user_id, network_id, node_id, status, expires_at)
  └─ RFC 8628 device authorization flow for interactive enrollment
  └─ status: pending → authorized → completed
  └─ 10-minute TTL, agent polls every 5 seconds

enrollment_bundles (id, node_id, download_token, downloaded, expires_at)
  └─ pre-generated tarballs for air-gapped/offline installs
  └─ single-use download URL, 15-minute TTL

audit_log (id, user_id, node_id, network_id, action, details, created_at)
  └─ actions: login, shell.connect, exec, port_forward.start, enroll
```

---

## API Endpoints (implemented)

### Public (no auth, rate limited)
| Method | Path | Handler | Purpose |
|---|---|---|---|
| GET | `/api/auth/status` | `AuthHandler.Status` | Check if any users exist |
| POST | `/api/auth/register` | `AuthHandler.Register` | Create account (email/password, 8-72 chars) |
| POST | `/api/auth/login` | `AuthHandler.Login` | Login → session token (hashed) + cookie |
| POST | `/api/enroll` | `EnrollHandler.Enroll` | Token-based enrollment (one-time token → certs) |
| POST | `/api/device/code` | `DeviceHandler.RequestCode` | Device flow: request device + user code |
| POST | `/api/device/poll` | `DeviceHandler.Poll` | Device flow: agent polls until authorized |
| GET | `/api/bundles/{token}` | `BundleHandler.DownloadBundle` | Download pre-generated enrollment tarball |

### Authenticated
| Method | Path | Handler | Purpose |
|---|---|---|---|
| POST | `/api/auth/logout` | `AuthHandler.Logout` | Destroy session |
| GET | `/api/auth/me` | `AuthHandler.Me` | Current user info |
| POST | `/api/networks` | `NetworkHandler.CreateNetwork` | Create network (auto CA + subnet) |
| GET | `/api/networks` | `NetworkHandler.ListNetworks` | List user's networks |
| GET | `/api/networks/{networkID}` | `NetworkHandler.GetNetwork` | Network detail + nodes (safe DTO) |
| DELETE | `/api/networks/{networkID}` | `NetworkHandler.DeleteNetwork` | Delete network + tunnels + forwards |
| POST | `/api/networks/{networkID}/nodes` | `EnrollHandler.CreateNode` | Generate enrollment token (10-min TTL) |
| GET | `/api/networks/{networkID}/nodes` | `ProxyHandler.ListNodes` | List nodes in network |
| GET | `/api/networks/{networkID}/nodes/{nodeID}/health` | `ProxyHandler.NodeHealth` | Agent health check via mesh |
| GET | `/api/networks/{networkID}/nodes/{nodeID}/shell` | `ProxyHandler.NodeShell` | WebSocket terminal via mesh (audited) |
| POST | `/api/networks/{networkID}/nodes/{nodeID}/exec` | `ProxyHandler.NodeExec` | Command exec, streaming (audited) |
| POST | `/api/networks/{networkID}/nodes/{nodeID}/port-forwards` | `ProxyHandler.StartPortForward` | Start TCP port forward (audited) |
| DELETE | `/api/networks/{networkID}/port-forwards/{fwdID}` | `ProxyHandler.StopPortForward` | Stop port forward (network-scoped) |
| GET | `/api/networks/{networkID}/port-forwards` | `ProxyHandler.ListPortForwards` | List active forwards |
| POST | `/api/device/authorize` | `DeviceHandler.Authorize` | Device flow: user authorizes code |
| GET | `/api/device/verify/{code}` | `DeviceHandler.VerifyCode` | Device flow: check code status |
| POST | `/api/networks/{networkID}/bundles` | `BundleHandler.CreateBundle` | Generate enrollment bundle tarball |

---

## Enrollment (4 modes)

See [docs/enrollment.md](enrollment.md) for detailed user flows and examples.

### Mode 1: Device Flow (Interactive, recommended)
```
Server: hop-agent enroll → shows code "HOP-K9M2"
Browser: user enters code at /device, selects network, authorizes
Server: agent polls /api/device/poll → receives certs → installs
```

### Mode 2: Token (Scriptable)
```
Dashboard: "Add Node" → generates token (10-min TTL, single-use, SHA-256 hashed)
Server: echo <token> | hop-agent enroll --token-stdin
```

### Mode 3: Bundle (Offline/Air-Gapped)
```
Dashboard: "Add Node" → "Download Bundle" → .tar.gz (15-min URL, single-use)
Server: hop-agent enroll --bundle /tmp/hop-bundle.tar.gz
```

### Mode 4: Terraform/Pulumi (IaC)
```
Terraform: hopssh_enrollment_token resource → injects into user-data
VM: cloud-init runs hop-agent enroll --token-stdin
```

### Common install path
```
curl -fsSL https://hopssh.com/install | sh    # downloads hop-agent + Nebula
sudo hop-agent enroll [--token-stdin | ...]    # enrolls via chosen mode
```

After enrollment:
```
Agent connects via Nebula (outbound UDP)
    │
    ▼
Node appears in dashboard (status: online)
    │ First health check updates last_seen_at
    │ enrollment_token consumed (set to NULL)
```

---

## Security Model

### Trust boundaries
```
┌────────────────────────────┐
│  Control Plane             │
│  Has: CA keys (encrypted), │
│       user sessions,       │
│       node tokens          │
│  Never has: SSH keys,      │
│       cloud credentials,   │
│       server passwords     │
└────────────┬───────────────┘
             │ Nebula (Noise Protocol)
             │ End-to-end encrypted
┌────────────▼───────────────┐
│  Agent                     │
│  Has: node cert/key,       │
│       agent token          │
│  Controls: server access   │
│  If stopped: control plane │
│       loses all access     │
└────────────────────────────┘
```

### Encryption layers
| Layer | Technology | What it protects |
|---|---|---|
| In transit | Nebula (Noise Protocol, Curve25519) | All agent ↔ control plane traffic |
| At rest (DB) | AES-256-GCM | CA keys, node keys, server keys, agent tokens |
| At rest (DB) | SHA-256 hash | Enrollment tokens (one-time, compare-only) |
| Auth (passwords) | bcrypt (DefaultCost) | User passwords (min 8 chars) |
| Per-network | Separate Curve25519 CA | Cryptographic isolation between networks |
| Agent auth | `subtle.ConstantTimeCompare` | Timing-safe token verification |

### Hardening measures
- **Secure cookies** — `HttpOnly`, `SameSite=Lax`, `Secure` (when HTTPS)
- **WebSocket origin validation** — Same-origin by default, configurable allowed origins
- **Request body limits** — 1 MB on all JSON endpoints, 100 MB on agent uploads
- **Upload path restriction** — Agent `/upload` restricted to `/var/hop-agent/uploads/`
- **Port forward IDs** — Crypto-random (not sequential), scoped to network on stop
- **Transactional migrations** — Each migration wrapped in a transaction
- **Data directory** — Created with `0700` permissions; encryption key file `0600`
- **Graceful shutdown** — Signal-based (SIGINT/SIGTERM) on both control plane and agent
- **ReadHeaderTimeout** — 10s on both control plane and agent (Slowloris protection)
- **Network deletion** — Cleans up active tunnels and port forwards before DB delete
- **Email validation** — Basic format check on registration (contains `@`, has domain with `.`)

### What makes it safe to offer as a service
1. **No stored credentials** — Control plane never holds SSH keys, cloud creds, or passwords
2. **Agent-mediated access** — If agent stops, access is impossible. Customer always in control.
3. **Per-network CA** — Compromising one network's CA has zero effect on other networks
4. **Outbound-only agent** — No inbound ports required on customer servers
5. **Token-based agent auth** — Per-node tokens, no shared secrets between nodes
6. **DB compromise resilience** — Agent tokens encrypted, enrollment tokens hashed. Raw DB access does not yield usable credentials.

---

## Mesh Networking Details

### Why Nebula (not WireGuard)
- **Userspace mode** — No kernel module needed on control plane. Uses gvisor's `overlay.NewUserDeviceFromConfig`.
- **Built-in PKI** — Native cert model (Curve25519 CA → node certs). No external CA infra.
- **NAT traversal** — UDP hole punching via `punchy.punch: true`.
- **Per-network isolation** — Each network has its own CA. Separate cryptographic domain.
- **MIT licensed** — No restrictions on commercial use or embedding.

### Tunnel lifecycle (implemented)
1. **On-demand creation** — Tunnel created when user opens terminal or starts port forward
2. **Handshake retry** — 3 attempts with 0s/15s/30s delays (handles session expiration after restart)
3. **TCP probe** — After tunnel creation, 12s TCP dial probe verifies handshake completion
4. **Idle reaper** — Background goroutine checks every 30s, closes tunnels idle >5 minutes
5. **Cache deduplication** — Concurrent requests for same node share one tunnel instance
6. **Connection serialization** — `sync.Map` + channel prevents concurrent retry loops per node

### Nebula vendor patch
`svc.Close()` triggers `os.Exit(2)` in upstream Nebula's `interface.go` (issue #1031). We apply a 1-line vendor patch that adds `io.ErrClosedPipe` to the error guard, allowing graceful shutdown. Tunnels now close cleanly. See `patches/` and `scripts/check-nebula-patch.sh`.

### Certificate rotation
Node certificates are short-lived (24h). The agent auto-renews at 50% lifetime (12h) by calling `POST /api/renew` with its bearer token. The control plane issues a fresh cert with the same identity. If the node has been deleted, renewal returns 401 and the agent shuts down — this provides effective certificate revocation without CRL infrastructure.

---

## Technology Choices

| Layer | Choice | Rationale |
|---|---|---|
| Language | Go 1.25 | Same as pulumi-ui, single static binary, no runtime deps |
| HTTP | chi router | Lightweight, middleware support, URL params |
| Database | SQLite (modernc.org/sqlite) | Pure Go, no CGO, embedded, zero ops |
| DB pattern | Dual read/write pool | PocketBase pattern: 120 readers + 1 writer, WAL mode |
| Mesh | Nebula v1.10.3 (slackhq) | Userspace, built-in PKI, MIT licensed |
| PTY | creack/pty | Battle-tested, minimal deps |
| WebSocket | gorilla/websocket | Standard Go WebSocket library |
| Encryption | AES-256-GCM (stdlib) | No external deps, NIST-approved |
| PKI | Curve25519 (ed25519 signing, x25519 key exchange) | Nebula's native curve |
| Auth | bcrypt + session cookies | Simple, proven, no JWT complexity |

---

## Scalability Path

| Nodes | Architecture | Write strategy | Database | Bottleneck |
|-------|-------------|---------------|----------|-----------|
| 0 – 10,000 | Single binary | `MaxOpenConns(1)` + lock retry | SQLite | None (33 writes/sec at 10K) |
| 10,000 – 100,000 | Single binary | Write channel + batching | SQLite | Write serialization (~3.3K/sec) |
| 100,000+ | Horizontal | Connection pool + replicas | PostgreSQL | Compute for Nebula instances |

**Current hardening (PocketBase-inspired):**
- Lock retry with escalating backoff (50ms → 3s) for "database is locked" errors
- Default 30-second query timeout on all operations
- Daily WAL checkpoint (`PRAGMA wal_checkpoint(TRUNCATE)`) + `PRAGMA optimize`
- Connection idle timeout (3 minutes), WAL journal size limit (200MB)
- Tunnels close properly via vendor patch (no more goroutine leak)

See [docs/roadmap.md](roadmap.md) for detailed scaling thresholds and migration triggers.
