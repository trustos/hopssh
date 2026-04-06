# hopssh — Architecture

Encrypted mesh networking with P2P, relay fallback, built-in DNS, and a web terminal.

---

## System Overview

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
│                                                              │
│  SQLite DB │ PKI (per-network CA) │ Audit log               │
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

### Key architectural decisions

- **One Nebula instance per network.** Each network has its own CA, lighthouse, relay, and DNS. Cryptographic isolation is enforced by separate CAs — nodes from different networks cannot handshake.
- **Control plane IS the lighthouse+relay.** No separate infrastructure. The single binary runs everything.
- **P2P primary, relay fallback.** ~92% of connections succeed as direct P2P via UDP hole punching. The remaining ~8% (symmetric NAT, strict firewalls) fall back to relay through the lighthouse.
- **Agents embed Nebula.** Single binary, no separate Nebula daemon. Connects persistently to the lighthouse.
- **Browser access through control plane.** Web terminal proxies through the control plane's mesh connection. Browsers can't join Nebula directly.

---

## Connection Flows

### P2P Direct (~92% of connections)
```
Agent A                    Lighthouse               Agent B
   │── register endpoint ──>│                          │
   │                         │<── register endpoint ──│
   │                         │                          │
   │── "where is B?" ──────>│                          │
   │<── "B is at 2.3.4.5" ──│                          │
   │                                                    │
   │<══════════ direct UDP P2P (hole punch) ══════════>│
   │           (Jellyfin, file sync, SSH — full speed)  │
```

### Relay Fallback (~8% — symmetric NAT, firewalls)
```
Agent A ──UDP──> Lighthouse/Relay ──UDP──> Agent B
              (E2E encrypted, relay is blind)
```

### Web Terminal (browser → agent, always through control plane)
```
Browser ──HTTPS/WSS──> Control Plane API ──Nebula──> Agent
                       (WebSocket proxy)   (mesh)    (PTY)
```

### Client Access (laptop/phone → server service)
```
Client (laptop)                          Agent (server)
   │── Nebula tunnel (P2P or relay) ───>│
   │                                     │
   │  curl http://jellyfin.zero:8096     │
   │──────────────────────────────────->│ :8096 (Jellyfin)
```

---

## Components

### Control Plane (`cmd/server`)

Single Go binary that runs:

| Component | Purpose |
|-----------|---------|
| **API server** (:9473 TCP) | Auth, network CRUD, enrollment, node management, DNS config |
| **Web dashboard** | Svelte 5 SPA, embedded in binary, served from same port |
| **NetworkManager** | Starts/stops persistent Nebula instances per network |
| **Lighthouse** (per network) | Peer discovery, endpoint registry |
| **Relay** (per network) | Forwards traffic when P2P fails (E2E encrypted, relay is blind) |
| **DNS server** (per network) | Resolves `hostname.domain` → mesh VPN IP |
| **SQLite** | All state: users, networks, nodes, certs, audit, DNS records |

### Agent (`cmd/agent serve`)

Installed on servers. Single binary with embedded Nebula:

| Component | Purpose |
|-----------|---------|
| **Nebula** (embedded, userspace) | Persistent mesh connection, registers with lighthouse |
| **HTTP server** (on VPN IP only) | /health, /exec, /shell, /upload — control plane access |
| **Service exposure** | Configurable ports accessible to mesh members (e.g., Jellyfin :8096) |
| **Cert renewal** | Auto-renews 24h certificates via HTTPS to control plane |

### Client (`cmd/agent client join`)

Installed on laptops/phones. Same binary, different mode:

| Component | Purpose |
|-----------|---------|
| **Nebula** (embedded, userspace) | Joins mesh, gets VPN IP, P2P to agents |
| **Split DNS** | Resolves `.zero` / `.prod` / etc. through mesh DNS |
| No HTTP server | Clients don't expose services (agents do) |

---

## Data Model

```sql
users (id, email, name, password_hash, github_id, created_at)
  └─ auth: email/password (bcrypt), GitHub OAuth (future)

sessions (token[hash], user_id, created_at, expires_at)
  └─ 30-day TTL, SHA-256 hashed at rest, cookie-based

api_keys (id, user_id, name, key_hash, last_used_at, created_at)
  └─ for CLI + Terraform provider (future)

networks (id, user_id, name, slug, nebula_ca_cert, nebula_ca_key[enc],
          nebula_subnet, server_cert, server_key[enc],
          lighthouse_port, dns_domain, created_at)
  └─ per-network Nebula CA (Curve25519), auto-allocated /24 subnet
  └─ lighthouse_port: unique UDP port for this network's Nebula instance
  └─ dns_domain: user-defined (e.g., "zero", "prod", "lab"), default "hop"
  └─ server cert = control plane's identity in this network (.1 IP)
  └─ CA key + server key AES-GCM encrypted at rest

nodes (id, network_id, hostname, os, arch, nebula_cert, nebula_key[enc],
       nebula_ip, agent_token[enc], enrollment_token[hash],
       enrollment_expires_at, agent_real_ip, node_type,
       exposed_ports, dns_name, status, last_seen_at, created_at)
  └─ node_type: "agent" (server), "user" (client), "lighthouse"
  └─ exposed_ports: JSON array of {port, proto, name} for mesh firewall
  └─ dns_name: custom hostname for DNS (defaults to hostname)
  └─ enrollment_token: SHA-256 hashed, single-use, 10-min TTL
  └─ agent_token: AES-GCM encrypted, constant-time comparison
  └─ nebula_key: AES-GCM encrypted
  └─ status: pending → enrolled → online → offline

device_codes (device_code[hash], user_code, user_id, network_id,
              node_id, status, expires_at, created_at)
  └─ RFC 8628 device authorization flow
  └─ status: pending → authorized → completed

enrollment_bundles (id, node_id, download_token[hash], downloaded,
                    expires_at, created_at)
  └─ pre-generated tarballs for air-gapped installs

dns_records (id, network_id, name, nebula_ip, created_at)
  └─ custom DNS records (beyond auto-generated hostname records)
  └─ e.g., "jellyfin" → 10.42.1.3 (shorthand for a service on a node)

audit_log (id, user_id, node_id, network_id, action, details, created_at)
  └─ actions: login, register, shell.connect, exec, port_forward.start,
              node.delete, network.create, dns.update
```

---

## DNS Resolution

### User-defined domains

Each network has a configurable DNS domain. Users choose it when creating the network:

```json
POST /api/networks
{ "name": "home", "dnsDomain": "zero" }
```

This creates DNS resolution like:
- `jellyfin.zero` → 10.42.1.3
- `nas.zero` → 10.42.1.4
- `immich.zero` → 10.42.1.5

### How it works

1. Each network's Nebula instance runs a DNS server on its lighthouse VPN IP
2. The DNS server resolves `<hostname>.<domain>` by looking up nodes in the database
3. Auto-generated records: every node with a hostname gets a record automatically
4. Custom records: users can add aliases (e.g., `jellyfin` pointing to a node's IP)
5. Client devices configure split DNS so only the mesh domain goes through mesh DNS

### Split DNS configuration (automatic on `hop client join`)

| Platform | Method | Example |
|----------|--------|---------|
| Linux (systemd-resolved) | `resolvectl domain` | `resolvectl domain nebula1 ~zero` |
| macOS | `/etc/resolver/<domain>` | File with `nameserver 10.42.1.1` |
| Windows | NRPT rule | PowerShell `Add-DnsClientNrptRule` |

Regular internet DNS is unaffected — only queries for the mesh domain go through the mesh.

---

## Firewall Groups

Nebula certificates carry groups. hopssh assigns groups based on node type:

| Group | Assigned to | Can access |
|-------|------------|------------|
| `admin` | Control plane (lighthouse) | All ports on all nodes |
| `agent` | Servers | Other agents (for future P2P services) |
| `user` | Client devices (laptops, phones) | Exposed service ports on agents |

### Agent firewall (generated during enrollment)

```yaml
firewall:
  inbound:
    # Control plane can reach agent management API
    - port: 41820
      proto: tcp
      groups: [admin]
    # Mesh members with "user" group can reach exposed services
    - port: 8096     # Jellyfin
      proto: tcp
      groups: [user]
    - port: 8000     # Paperless
      proto: tcp
      groups: [user]
    # ICMP for diagnostics
    - port: any
      proto: icmp
      host: any
  outbound:
    - port: any
      proto: any
      host: any
```

Exposed ports are configurable per node via the dashboard.

---

## API Endpoints

### Public (no auth, rate limited)
| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/auth/status` | Check if any users exist |
| POST | `/api/auth/register` | Create account |
| POST | `/api/auth/login` | Login → session cookie |
| POST | `/api/enroll` | Token-based agent enrollment |
| POST | `/api/device/code` | Device flow: request code |
| POST | `/api/device/poll` | Device flow: agent polls |
| POST | `/api/renew` | Agent cert renewal (bearer token auth) |
| GET | `/api/bundles/{token}` | Download enrollment bundle (HTTPS required) |

### Authenticated (session cookie)
| Method | Path | Purpose |
|--------|------|---------|
| POST | `/api/auth/logout` | Destroy session |
| GET | `/api/auth/me` | Current user info |
| **Networks** | | |
| POST | `/api/networks` | Create network (+ start lighthouse, allocate UDP port) |
| GET | `/api/networks` | List user's networks |
| GET | `/api/networks/{id}` | Network detail + nodes |
| DELETE | `/api/networks/{id}` | Delete network (+ stop lighthouse, cleanup) |
| **Nodes** | | |
| POST | `/api/networks/{id}/nodes` | Generate enrollment token |
| GET | `/api/networks/{id}/nodes` | List nodes in network |
| DELETE | `/api/networks/{id}/nodes/{nodeId}` | Delete node (revoke access) |
| GET | `/api/networks/{id}/nodes/{nodeId}/health` | Health check via mesh |
| GET | `/api/networks/{id}/nodes/{nodeId}/shell` | WebSocket terminal via mesh |
| POST | `/api/networks/{id}/nodes/{nodeId}/exec` | Command exec (streaming) |
| PUT | `/api/networks/{id}/nodes/{nodeId}/ports` | Configure exposed service ports |
| **Port Forwards** | | |
| POST | `/api/networks/{id}/nodes/{nodeId}/port-forwards` | Start port forward |
| DELETE | `/api/networks/{id}/port-forwards/{fwdId}` | Stop port forward |
| GET | `/api/networks/{id}/port-forwards` | List active forwards |
| **DNS** | | |
| GET | `/api/networks/{id}/dns` | List DNS records |
| POST | `/api/networks/{id}/dns` | Add custom DNS record |
| DELETE | `/api/networks/{id}/dns/{recordId}` | Remove DNS record |
| **Mesh** | | |
| POST | `/api/networks/{id}/join` | Client joins network (issues "user" cert) |
| GET | `/api/networks/{id}/peers` | List connected peers with P2P/relay status |
| **Device Flow** | | |
| POST | `/api/device/authorize` | User authorizes device code |
| GET | `/api/device/verify/{code}` | Check device code status |
| **Bundles** | | |
| POST | `/api/networks/{id}/bundles` | Generate enrollment bundle |

---

## Enrollment

See [enrollment.md](enrollment.md) for detailed user flows and examples.

### Agent enrollment (servers)
Four modes: device flow, token stdin, token argument, pre-bundled tarball.
All modes issue a Nebula certificate with group `agent` and return lighthouse endpoint info.

### Client join (laptops/phones)
```
hop client join --network <id> --endpoint https://hopssh.com
```
Authenticates via browser, gets a Nebula cert with group `user`, starts embedded Nebula, configures split DNS.

---

## Security Model

### Encryption layers
| Layer | Technology | What it protects |
|-------|------------|------------------|
| Mesh transit | Nebula (Noise Protocol, Curve25519) | All node-to-node traffic (P2P and relayed) |
| At rest (DB) | AES-256-GCM | CA keys, node keys, server keys, agent tokens |
| At rest (DB) | SHA-256 hash | Session tokens, enrollment tokens, device codes, bundle tokens |
| Passwords | bcrypt (DefaultCost) | User passwords (8-72 chars) |
| Network isolation | Separate Curve25519 CA per network | Nodes from different networks cannot communicate |
| Agent auth | `subtle.ConstantTimeCompare` | Timing-safe bearer token verification |

### Trust boundaries
```
┌─────────────────────────────────┐
│  Control Plane                   │
│  Has: CA keys (encrypted),       │
│       node tokens, session data  │
│  IS: lighthouse, relay, DNS      │
│  Relay is BLIND — cannot decrypt │
│       node-to-node traffic       │
│  Never has: SSH keys, cloud      │
│       credentials, passwords     │
└──────────┬──────────┬────────────┘
           │ (mesh)   │ (mesh)
     ┌─────▼────┐ ┌───▼──────┐
     │  Agent   │ │  Client  │
     │  Has:    │ │  Has:    │
     │  cert,   │ │  cert    │
     │  token   │ │  (user)  │
     │  (agent) │ │          │
     └──────────┘ └──────────┘
         ↕ P2P (direct or relayed)
```

### What makes it safe
1. **E2E encryption** — relay cannot read traffic. Only endpoints with valid certs can communicate.
2. **Per-network CA** — compromising one network's CA has zero effect on others.
3. **Short-lived certs (24h)** — auto-renewed. Node deletion = cert not renewed = access revoked within 24h.
4. **Firewall groups** — agents, users, and admins have different access levels enforced by Nebula certs.
5. **No inbound ports** — agents and clients connect outbound to the lighthouse.
6. **Single binary** — no dependency chain, no supply chain attack surface beyond Go stdlib + Nebula.

---

## Technology Choices

| Layer | Choice | Rationale |
|-------|--------|-----------|
| Language | Go 1.24 | Single static binary, no runtime deps, strong concurrency |
| Mesh | Nebula v1.10.3 (vendor patched) | Userspace, built-in PKI, relay (v1.6+), MIT licensed |
| HTTP | chi router | Lightweight, middleware, URL params |
| Database | SQLite (modernc.org/sqlite) | Pure Go, no CGO, embedded, zero ops |
| DB queries | sqlc | Type-safe generated Go code from SQL files |
| DB pattern | Dual read/write pool + ResilientDB | PocketBase pattern: 20 readers + 1 writer, lock retry |
| Frontend | Svelte 5 + shadcn-svelte + Tailwind | Fast, modern, embedded in Go binary |
| Terminal | xterm.js | Standard web terminal emulator |
| WebSocket | gorilla/websocket | Standard Go WebSocket library |
| PTY | creack/pty | Battle-tested, minimal deps |
| Encryption | AES-256-GCM (stdlib) | NIST-approved, no external deps |
| PKI | Curve25519 (ed25519 + x25519) | Nebula's native curve |
| Auth | bcrypt + HttpOnly session cookies | Simple, proven, no JWT complexity |
| DNS | miekg/dns | Standard Go DNS library |

---

## Scalability Path

### Single server (0 – 10,000 nodes)

One binary does everything: API, web UI, lighthouse, relay, DNS, SQLite.

- **Lighthouse memory**: ~1KB per node. 10,000 nodes = 10MB.
- **Lighthouse bandwidth**: ~400KB/s for 10,000 nodes at 10s update interval.
- **Relay bandwidth**: ~6-8% of connections need relay. Terminal sessions are 2-5 KB/s each.
- **SQLite**: 33 writes/sec at 10K nodes (health check updates). Well within limits.
- **Hardware**: $20-40/month VPS (4 CPU, 8GB RAM, 1Gbps) is dramatically overprovisioned.

### Regional relays (10,000 – 100,000 nodes)

Add standalone Nebula relay nodes in different regions. No application logic — just Nebula config:
```yaml
lighthouse:
  am_lighthouse: false
relay:
  am_relay: true
```

Control plane remains single server. Relay nodes reduce latency for cross-region relayed connections.

### Horizontal (100,000+ nodes)

- PostgreSQL replaces SQLite
- Multiple control plane instances behind load balancer
- Regional lighthouses (one per region, synced via control plane)
- Dedicated relay fleet

See [roadmap.md](roadmap.md) for detailed scaling thresholds.

---

## Nebula Vendor Patch

We apply a 1-line patch to `vendor/github.com/slackhq/nebula/interface.go` to fix `os.Exit(2)` on
service shutdown (issue [#1031](https://github.com/slackhq/nebula/issues/1031)). The fix adds
`io.ErrClosedPipe` to the error guard so userspace Nebula instances close gracefully.

- **Patch**: `patches/nebula-1031-graceful-shutdown.patch`
- **Apply**: `make vendor` (automatic)
- **Monitor**: `scripts/check-nebula-patch.sh` (checks if upstream PR [#1375](https://github.com/slackhq/nebula/pull/1375) has merged)
