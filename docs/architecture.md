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
│  │  ├─ /upload   → file upload to arbitrary path         │    │
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
  agent/main.go          Agent binary: /health, /exec, /upload, /shell (350 lines)
  server/main.go         Control plane: wires DB, mesh, handlers, serves HTTP (100 lines)

internal/
  api/
    router.go            Chi router: public + auth-protected route groups
    auth.go              Register, login, logout, me, status (bcrypt, 30-day sessions)
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
    nodes.go             NodeStore: CRUD, enrollment completion, status/lastSeen updates
    audit.go             AuditStore: log entries, list for network
    migrations/
      001_initial.sql    6 tables, 8 indexes (users, sessions, api_keys, networks, nodes, audit_log)
  mesh/
    mesh.go              Nebula tunnel manager: per-node tunnels, retry, idle reaper (from pulumi-ui, adapted)
    forward.go           TCP port forwarding: local listener → mesh tunnel → remote port
  pki/
    pki.go               Nebula CA generation + cert issuance (Curve25519/ed25519)
    subnet.go            Subnet IP allocation helpers

install.sh               One-liner agent installer (curl | bash)

docs/
  architecture.md        This file
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
       nebula_ip, agent_token, enrollment_token, agent_real_ip,
       status, last_seen_at, created_at)
  └─ enrollment_token: one-time, consumed on enroll (set to NULL)
  └─ agent_token: per-node Bearer token for agent auth
  └─ status: pending → online → offline
  └─ nebula_key AES-GCM encrypted at rest

audit_log (id, user_id, node_id, network_id, action, details, created_at)
  └─ actions: connect, disconnect, port_forward, enroll
```

---

## API Endpoints (implemented)

### Public (no auth)
| Method | Path | Handler | Purpose |
|---|---|---|---|
| GET | `/api/auth/status` | `AuthHandler.Status` | Check if any users exist |
| POST | `/api/auth/register` | `AuthHandler.Register` | Create account (email/password) |
| POST | `/api/auth/login` | `AuthHandler.Login` | Login → session token + cookie |
| POST | `/api/enroll` | `EnrollHandler.Enroll` | Agent enrollment (one-time token → certs) |

### Authenticated
| Method | Path | Handler | Purpose |
|---|---|---|---|
| POST | `/api/auth/logout` | `AuthHandler.Logout` | Destroy session |
| GET | `/api/auth/me` | `AuthHandler.Me` | Current user info |
| POST | `/api/networks` | `NetworkHandler.CreateNetwork` | Create network (auto CA + subnet) |
| GET | `/api/networks` | `NetworkHandler.ListNetworks` | List user's networks |
| GET | `/api/networks/{networkID}` | `NetworkHandler.GetNetwork` | Network detail + nodes |
| DELETE | `/api/networks/{networkID}` | `NetworkHandler.DeleteNetwork` | Delete network + all nodes |
| POST | `/api/networks/{networkID}/nodes` | `EnrollHandler.CreateNode` | Generate enrollment token |
| GET | `/api/networks/{networkID}/nodes` | `ProxyHandler.ListNodes` | List nodes in network |
| GET | `/api/networks/{networkID}/nodes/{nodeID}/health` | `ProxyHandler.NodeHealth` | Agent health check via mesh |
| GET | `/api/networks/{networkID}/nodes/{nodeID}/shell` | `ProxyHandler.NodeShell` | WebSocket terminal via mesh |
| POST | `/api/networks/{networkID}/nodes/{nodeID}/exec` | `ProxyHandler.NodeExec` | Command exec (streaming) |
| POST | `/api/networks/{networkID}/nodes/{nodeID}/port-forwards` | `ProxyHandler.StartPortForward` | Start TCP port forward |
| DELETE | `/api/networks/{networkID}/port-forwards/{fwdID}` | `ProxyHandler.StopPortForward` | Stop port forward |
| GET | `/api/networks/{networkID}/port-forwards` | `ProxyHandler.ListPortForwards` | List active forwards |

---

## Enrollment Flow (implemented)

```
Dashboard: user clicks "Add Node"
    │
    ▼
POST /api/networks/{id}/nodes
    │ Creates node with:
    │ - enrollment_token (one-time, 32-byte hex)
    │ - agent_token (permanent, 32-byte hex)
    │ - nebula_ip (next available in /24 subnet)
    │ Returns: { installCommand: "curl ... --token <enrollment_token>" }
    │
    ▼
User pastes on server:
    curl -fsSL https://hopssh.com/install | sudo bash -s -- --token <token>
    │
    │ install.sh:
    │ 1. Downloads Nebula + hop-agent binaries
    │ 2. POST /api/enroll { token, hostname, os, arch }
    │     └─ Server: validates token → issues node cert → returns certs + agent token
    │ 3. Writes certs to /etc/hop-agent/
    │ 4. Writes Nebula config with static_host_map → control plane
    │ 5. Creates + starts systemd services (nebula, hop-agent)
    │
    ▼
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
| At rest (DB) | AES-256-GCM | CA keys, node keys, server keys |
| Auth (passwords) | bcrypt (DefaultCost) | User passwords |
| Per-network | Separate Curve25519 CA | Cryptographic isolation between networks |

### What makes it safe to offer as a service
1. **No stored credentials** — Control plane never holds SSH keys, cloud creds, or passwords
2. **Agent-mediated access** — If agent stops, access is impossible. Customer always in control.
3. **Per-network CA** — Compromising one network's CA has zero effect on other networks
4. **Outbound-only agent** — No inbound ports required on customer servers
5. **Token-based agent auth** — Per-node tokens, no shared secrets between nodes

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

### Known Nebula limitation
`svc.Close()` triggers `os.Exit(2)` race in Nebula's `interface.go`. Tunnels are removed from cache but the Nebula service is left running (~100KB goroutines). Goroutines exit when server process exits. Acceptable for MVP.

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

| Scale | Architecture | Nodes | Bottleneck |
|---|---|---|---|
| MVP | Single binary, SQLite | ~500 | Nebula tunnels per-process (~100KB each) |
| Growth | Batched writes, log files | ~2,000 | SQLite single writer |
| Scale | PostgreSQL, horizontal | Unlimited | Compute for Nebula instances |

The single-binary SQLite architecture handles the first 500+ paying nodes.
Each tunnel consumes ~100KB of memory (Nebula goroutines + service wrapper).
A 1GB-RAM server can sustain ~1,000 concurrent tunnels.
