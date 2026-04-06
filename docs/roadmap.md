# hopssh — Development Roadmap

## Vision

hopssh is an encrypted mesh networking platform — a ZeroTier/Tailscale alternative with
the best self-hosted experience. Single binary, zero infrastructure, built-in web terminal.

---

## Phase 1: Mesh Networking Core

The foundation. P2P mesh with relay fallback. This is the core product.

### Networking
| Task | Status | Notes |
|------|--------|-------|
| Persistent NetworkManager (per-network Nebula instances) | Planned | Replace ephemeral tunnel manager |
| Lighthouse + relay per network | Planned | Control plane IS the lighthouse |
| P2P direct connections (UDP hole punching) | Planned | Nebula `punchy.punch: true` |
| Relay fallback through lighthouse | Planned | Nebula relay (v1.6+) |
| Agent with embedded persistent Nebula | Planned | Single binary, always connected |
| Client app (`hop client join`) | Planned | Laptops/phones join the mesh |
| Built-in DNS (user-defined domains) | Planned | `jellyfin.zero`, `db.prod` |
| Split DNS on clients (Linux/macOS/Windows) | Planned | Only mesh domain goes through mesh |
| Service exposure config (ports per node) | Planned | Dashboard configurable |
| Firewall groups (agent/user/admin) | Planned | Via Nebula cert groups |

### Already Implemented
| Feature | Status |
|---------|--------|
| Agent enrollment (device flow, token, bundle) | ✓ |
| Web terminal (browser shell via mesh) | ✓ |
| Port forwarding (TCP tunnel) | ✓ |
| Node health dashboard | ✓ |
| Networks (isolated per-network CA) | ✓ |
| Short-lived certs (24h) + auto-renewal | ✓ |
| Audit logging | ✓ |
| Rate limiting, CORS, security hardening | ✓ |
| sqlc type-safe queries | ✓ |
| SQLite production hardening (PocketBase-inspired) | ✓ |
| Svelte 5 frontend scaffold (hop theme) | ✓ |
| Nebula vendor patch (graceful shutdown) | ✓ |
| CI, Docker, Makefile | ✓ |

**Deliverable:** Working mesh network. Install agent on servers, join from laptop,
reach services by name. Web terminal works. Self-hosted in one binary.

---

## Phase 2: Teams + Management

Convert solo users into teams. Polish the dashboard.

| Task | Priority |
|------|----------|
| Team invitations (email invite → mesh access) | P0 |
| ACL rules (fine-grained access beyond groups) | P0 |
| Peer connectivity map (P2P vs relayed visualization) | P1 |
| Regional relay nodes (add via dashboard) | P1 |
| API keys for automation | P1 |
| GitHub OAuth login | P1 |
| Stripe billing (per-node pricing) | P0 |
| Landing page (hopssh.com) | P0 |
| Documentation site | P1 |

**Deliverable:** Team tier live. Invite colleagues, manage access, billing.

---

## Phase 3: Enterprise + Scale

| Task | Priority |
|------|----------|
| SSO / SAML | P1 |
| RBAC (granular permissions) | P1 |
| Session recording | P1 |
| Desktop tray app (macOS, Windows, Linux) | P1 |
| Mobile clients (iOS, Android) | P2 |
| Terraform/Pulumi provider | P1 |
| Bandwidth monitoring per network | P2 |
| Webhook notifications (node online/offline) | P2 |
| SOC 2 compliance documentation | P2 |

**Deliverable:** Enterprise-ready. SSO, audit, compliance.

---

## Scaling Thresholds

### Current architecture (handles ~10,000 nodes)

Single binary: API + web UI + SQLite + lighthouse + relay + DNS.

**Why this works:**
- Lighthouse memory: ~1KB per node. 10,000 nodes = 10MB.
- Lighthouse bandwidth: ~400KB/s for 10K nodes.
- Relay: only ~8% of connections use relay. Terminal sessions are 2-5 KB/s.
- SQLite: 33 writes/sec at 10K nodes (health updates). Well within WAL limits.
- Hardware: $20-40/month VPS is dramatically overprovisioned.

**Hardening (implemented):**
- Lock retry with escalating backoff (PocketBase pattern)
- Daily WAL checkpoint + PRAGMA optimize
- Connection idle timeout, journal size limit
- Nebula tunnels close properly (vendor patch)

### ~10,000 nodes: Add regional relays

Standalone Nebula relay nodes in different regions. No app logic — just Nebula config.
Reduces latency for relayed cross-region connections. Control plane stays single server.

### ~100,000 nodes: Write channel + batching

Replace implicit `MaxOpenConns(1)` serialization with explicit write channel.
Batch `UpdateLastSeen` for N nodes into single statement.

**Why not now:** PocketBase proves the simple pattern handles production.
Add complexity only when monitoring demands it.

### ~100,000+ nodes: PostgreSQL

Swap SQLite for PostgreSQL. Multiple control plane instances behind load balancer.
Regional lighthouses. Dedicated relay fleet.

**Why not now:** PostgreSQL adds operational burden. SQLite in a single binary is zero-ops.

### Node count to architecture mapping

| Nodes | Control Plane | Relay | Database |
|-------|--------------|-------|----------|
| 0 – 10,000 | Single binary | Embedded in control plane | SQLite |
| 10K – 100K | Single binary | Regional relay nodes | SQLite + write batching |
| 100K+ | Horizontal (load balanced) | Relay fleet | PostgreSQL |

---

## Self-Hosted vs Hosted

### Self-hosted (default, free forever)

User runs the single binary on their server. All features included.
One server handles everything: API, dashboard, lighthouse, relay, DNS.

```bash
make setup && make build-all
./hop-server --endpoint https://my-server.com
# Open https://my-server.com:9473
# Create network, enroll servers, join from laptop
```

### Hosted (hopssh.com)

hopssh runs the infrastructure. Users just install agents and join.

| Tier | Price | Includes |
|------|-------|---------|
| Free | $0 | 10 nodes, 1 network, P2P + relay |
| Pro | $5/node/month | Unlimited networks, DNS, web terminal, audit, API |
| Enterprise | Contact | SSO, RBAC, session recording, regional relays, SLA |

---

## Launch Checklist

- [ ] Phase 1 networking complete (P2P + relay + DNS + client)
- [ ] Deployed on a VPS with public IP
- [ ] Domain: hopssh.com with TLS
- [ ] Agent binary on GitHub Releases (linux/amd64, linux/arm64)
- [ ] Client binary on GitHub Releases (linux, macOS, Windows)
- [ ] Landing page with sign-up or self-hosted download
- [ ] Demo video: "60 seconds from install to Jellyfin"
- [ ] Blog post: "Why we built hopssh"
- [ ] Documentation site
