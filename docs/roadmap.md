# hopssh — Development Roadmap

## Phase 0: Extract & Foundation (Week 1-2)

Extract core packages from pulumi-ui, adapt for standalone use.

| Task | Source | Notes |
|---|---|---|
| Agent binary | `pulumi-ui/cmd/agent/` | Strip Nomad-specific endpoints, keep health + shell + TCP relay |
| Mesh manager | `pulumi-ui/internal/mesh/` | As-is (tunnel lifecycle, idle reaper, per-node) |
| PKI | `pulumi-ui/internal/nebula/` | As-is (CA generation, cert issuance) |
| Crypto | `pulumi-ui/internal/crypto/` | As-is (AES-256-GCM + passphrase-based) |
| SQLite stores | `pulumi-ui/internal/db/` | New schema: users, networks, nodes, audit_log |
| Auth | `pulumi-ui/internal/auth/` | Adapt for API keys + OAuth |

**Deliverable:** Go project that compiles, with agent + control plane skeleton.

## Phase 1: Core MVP (Week 3-4)

Minimum product that a solo dev can use end-to-end.

| Feature | Priority | Status |
|---|---|---|
| GitHub OAuth login | P0 | |
| Create network (auto PKI) | P0 | |
| "Add Node" → enrollment token | P0 | |
| Install script (`hopssh.com/install`) | P0 | |
| Agent enrollment endpoint | P0 | |
| Node appears in dashboard (online/offline) | P0 | |
| Web terminal through mesh | P0 | |
| Port forwarding (start/stop/list) | P0 | |
| Node health (OS, uptime, status) | P0 | |

**Deliverable:** Working product. One user, one network, browser terminal works.

## Phase 2: Polish & Launch (Week 5-6)

Ready for public launch (HN Show, blog post, Twitter).

| Feature | Priority | Status |
|---|---|---|
| Landing page (hopssh.com) | P0 | |
| Hosted control plane deployment | P0 | |
| Free tier enforcement (5 nodes, 1 network) | P0 | |
| Install script auto-detects OS/arch | P0 | |
| Agent auto-reconnect on network change | P1 | |
| Terminal resize, color, copy/paste | P1 | |
| Node detail page (services, uptime, connections) | P1 | |
| `hop` CLI: enroll, status, networks | P1 | |
| Documentation site | P1 | |

**Deliverable:** Public launch. Free tier available. Landing page live.

## Phase 3: Team Features (Week 7-10)

Convert solo devs into paying teams.

| Feature | Priority | Status |
|---|---|---|
| Email invitations to network | P0 | |
| Team member roles (admin/member/viewer) | P0 | |
| Audit log (who connected when) | P0 | |
| Access revocation (remove user → instant cutoff) | P0 | |
| Multiple networks per account | P0 | |
| API keys for automation | P1 | |
| Stripe billing ($5/node/month) | P0 | |
| Usage dashboard (nodes, connections, history) | P1 | |

**Deliverable:** Team tier live. First paying customers.

## Phase 4: Growth (Week 11-16)

Expand distribution and feature set.

| Feature | Priority | Status |
|---|---|---|
| Terraform provider (`hopssh_network`) | P1 | |
| Pulumi provider (bridged from Terraform) | P2 | |
| `hop enroll` CLI with SSH-based batch install | P1 | |
| Ansible role for agent deployment | P2 | |
| Email/password auth (in addition to OAuth) | P1 | |
| Custom branding for networks | P2 | |
| Webhook notifications (node online/offline) | P2 | |

## Phase 5: Enterprise (Month 4+)

Only if demand warrants it.

| Feature | Priority | Status |
|---|---|---|
| SSO / SAML | P1 | |
| RBAC (granular permissions) | P1 | |
| Session recording | P1 | |
| Self-hosted distribution (Docker image) | P1 | |
| SOC 2 compliance documentation | P2 | |
| SLA + priority support | P2 | |

## Scaling Thresholds

The control plane uses a single-binary SQLite architecture. This section documents
the known scaling limits and the changes needed at each threshold.

### Current architecture (handles ~100,000 nodes)

**SQLite write path:** Single writer (`MaxOpenConns=1`) with WAL mode. Writes are
serialized through Go's `database/sql` connection pool — no explicit channels or
mutexes. PocketBase uses the identical pattern and serves thousands of concurrent
users at production scale.

**Why this works:** The heaviest write path is `UpdateLastSeen`, called once per
health check poll. At 30-second polling intervals:
- 1,000 nodes = 33 writes/sec (trivial)
- 10,000 nodes = 333 writes/sec (comfortable)
- 100,000 nodes = 3,333 writes/sec (near SQLite WAL limit of ~5-10K writes/sec)

**Hardening (implemented):**
- Lock retry with escalating backoff (50ms → 3s, 12 attempts) for "database is locked"
- Default 30-second query timeout on all operations
- Daily WAL checkpoint + PRAGMA optimize
- Connection idle timeout (3 minutes)
- WAL journal size limit (200MB)

### ~100,000 nodes: Write channel + batching

**When to implement:** When monitoring shows write queue latency exceeding 100ms
or "database is locked" retries becoming frequent.

**What to do:**
- Replace implicit `MaxOpenConns(1)` serialization with an explicit write channel
- Single goroutine consumes from the channel, batches compatible writes
- `UpdateLastSeen` for N nodes becomes 1 batch `UPDATE ... WHERE id IN (?...)`
  instead of N individual statements
- Priority queue: enrollment/auth writes before health check writes
- Metrics: queue depth, write latency, batch sizes

**Why not now:** The write channel adds ~200 lines of synchronization code, a new
failure mode (channel full/blocked), and makes transactions harder to reason about.
PocketBase proves the simple pattern handles production load. We add complexity only
when monitoring demands it.

### ~100,000+ nodes: PostgreSQL migration

**When to implement:** When SQLite's single-writer fundamentally limits throughput,
or when horizontal scaling is needed (multiple control plane instances).

**What to do:**
- Swap `modernc.org/sqlite` for `pgx` driver
- All SQL queries are already in `.sql` files (sqlc) — port SQLite-specific syntax
  (e.g., `unixepoch()`, `PRAGMA`) to PostgreSQL equivalents
- The `DBPair` abstraction becomes a connection pool to the same PostgreSQL instance
  (reads can go to replicas)
- The `authz` package already abstracts authorization — team/RBAC queries go here

**Why not now:** PostgreSQL adds operational burden (provisioning, backups, monitoring,
connection management). SQLite in a single binary is zero-ops. The entire database is
one file that can be backed up with `cp`.

### Node count to architecture mapping

| Nodes | Architecture | Write strategy | Database |
|-------|-------------|---------------|----------|
| 0 – 10,000 | Single binary | `MaxOpenConns(1)` + lock retry | SQLite |
| 10,000 – 100,000 | Single binary | Write channel + batching | SQLite |
| 100,000+ | Horizontal | Connection pool + replicas | PostgreSQL |

## Launch Checklist

- [ ] Domain: hopssh.com (bought)
- [ ] GitHub org or repo: trustos/hopssh
- [ ] Landing page with waitlist or direct sign-up
- [ ] Install script hosted at hopssh.com/install
- [ ] Control plane deployed (single binary on VPS or container)
- [ ] Agent binaries on GitHub Releases (linux/amd64, linux/arm64)
- [ ] Demo video: "60 seconds from install to terminal"
- [ ] Blog post: "Why we built hopssh"
- [ ] HN Show post
- [ ] Twitter/X thread
