# hopssh — Product Roadmap

*Last updated: 2026-04-14*

---

## Current State

hopssh is a working encrypted mesh networking platform. Phase 1 (mesh core) is complete. See [features.md](features.md) for the full inventory of shipped capabilities.

**What ships today:**
- Self-hosted single binary (API + web UI + SQLite + lighthouse + relay + DNS)
- P2P mesh networking on Nebula with per-network CA isolation
- Web terminal (browser-based PTY through the mesh)
- Port forwarding and HTTP proxy to node-local services
- 4 enrollment modes (device flow, token, bundle, client join)
- Built-in DNS with user-defined domains per network
- Teams with invite links, admin/member roles, network sharing
- Audit logging, real-time WebSocket events, self-update
- Docker, systemd, launchd, non-root agent, cross-platform releases

---

## Competitive Position

See [competitive-analysis.md](competitive-analysis.md) for the full matrix.

**Where we win today:**
- Self-hosted single binary (DN is SaaS-only, Tailscale is SaaS, ZeroTier needs separate controller)
- Full web terminal with xterm-256color PTY (Tailscale has a limited SSH Console; DN and ZeroTier have nothing)
- Bundled lighthouse + relay with zero customer infrastructure (DN requires customer-hosted lighthouses and relays; Tailscale and ZeroTier bundle their own relay infrastructure too)
- Built-in DNS with user-defined domains (DN has none, ZeroTier's is incomplete, Tailscale is locked to .ts.net)
- HTTP proxy to node-local services (unique — no competitor has browser-based reverse proxy through the mesh)
- Single-binary self-hosted deployment (vs DN's SaaS + customer-hosted lighthouses; Tailscale and ZeroTier users get zero-infra but it's SaaS-dependent)

**Where we lose today:**
- No SSO/OIDC (DN, Tailscale, ZeroTier all have it)
- No scoped API keys (DN and Tailscale have them)
- No granular firewall rules (DN has roles+tags, Tailscale has ACLs+grants, ZeroTier has flow rules)
- No mobile or desktop apps (all three competitors have them)
- No webhooks, log streaming, or Terraform provider (Tailscale leads here)
- No subnet routing or exit nodes (Tailscale and ZeroTier have them)
- No session recording (Tailscale has it)
- Smaller free tier (10 nodes vs DN's 100, ZeroTier's 25)

---

## Implementation Roadmap

Every feature is numbered and ordered by impact — highest value to the product first. Each entry has a complexity estimate (S = days, M = 1-2 weeks, L = 2-4 weeks), a viability assessment based on codebase review, and a "why" tied to competitive analysis or market need.

### Phase 2A: Quick Wins & Foundation

High-impact features with low effort. Ship these first to accelerate adoption and unblock later features.

| # | Feature | Description | Why | Size | Viability | Depends on |
|---|---------|-------------|-----|------|-----------|------------|
| 1 | **Expand free tier to 25 nodes** | Increase from 10 to 25 nodes on the free tier. | Our 10-node free tier is the smallest in market. DN offers 100, ZeroTier offers 25. 25 is enough for serious evaluation without killing conversion. Immediate adoption impact. | S | Trivial — config change only | — |
| 2 | **Webhooks** | Send HTTP webhooks on events (node online/offline, enrollment, member changes, audit events). Configurable per network with retry and HMAC signing. | Tailscale and ZeroTier both have webhooks. Required for any integration story (Slack alerts, PagerDuty, custom automation). The internal `EventHub` pub/sub system is already built and publishing 8+ event types — webhooks just add an HTTP delivery layer on top. | M | Very high — event system is 90% done. Add `network_webhooks` table, subscribe to EventHub, POST with retry. ~400-600 LOC. | — |
| 3 | **GitHub OAuth** | Add GitHub as a login provider. OAuth redirect + callback handler, user lookup/creation by `github_id`. | Table stakes for developer-facing product. Tailscale and ZeroTier have it. Lowest friction auth for devs. | S | High — `github_id` column already exists in `users` table, session creation path is reusable. ~200-300 LOC. | — |
| 4 | **Scoped API keys** | Implement API key creation, listing, deletion, and scoped permissions. Add `scopes` column to existing `api_keys` table. Auth middleware to validate keys alongside sessions. | DN has scoped API keys with OpenAPI spec. Needed for any automation story. Unblocks Terraform provider (#10). | M | Moderate — `api_keys` table exists but needs `scopes` column. Need new middleware alongside existing `RequireAuth`. ~500-800 LOC. | — |

### Phase 2B: Beat Defined Networking

Close the gaps that make DN look more complete. After this phase, hopssh is decisively better than DN on every dimension except mobile apps and architecture breadth.

| # | Feature | Description | Why | Size | Viability | Depends on |
|---|---------|-------------|-----|------|-----------|------------|
| 5 | **SSO/OIDC** | Add OIDC login (Google, Microsoft, Okta, custom). Standard OpenID Connect discovery + token exchange. | DN, Tailscale, and ZeroTier all have SSO. Single biggest gap keeping us from enterprise conversations. Opens the door to all identity-aware features. | L | Moderate — no OIDC code exists today. Session system is simple enough to extend (create user from ID token claims, reuse session creation). Needs `golang.org/x/oauth2` dependency. ~800-1200 LOC. | — |
| 6 | **Granular firewall rules** | Tag-based firewall: assign tags (key:value) to nodes, define rules between tag groups on specific ports/protocols. Compile to Nebula cert groups + firewall config. UI for rule management with preview. | DN has roles + tags + firewall builder. Current Nebula config is wide-open allow-all. This is the foundation for all access control. | L | Moderate — Nebula supports group-based firewall natively. Currently certs are issued with hardcoded `groups: ["node"]`. Need: tags table, rule generation, cert re-issuance. Caveat: tag changes require cert renewal (24h cycle, or add immediate renewal endpoint). ~400-600 LOC. | — |
| 7 | **Subnet routing** | Allow designated nodes to route traffic to non-overlay subnets (LAN, cloud VPCs). Dashboard UI to configure routes per node. | DN has 2 routes (free) / 100 routes (pro). Tailscale and ZeroTier both have it. Required for accessing existing LAN resources through the mesh. | M | High — Nebula supports `unsafe_routes` natively, just not exposed. Add `routes` column to networks, generate route config in agent Nebula template. Note: routing node must run as root. ~300-500 LOC. | — |
| 8 | **Exit nodes** | Designate a node as an exit node to route all traffic (or specific domains) through the mesh. | Tailscale has exit nodes. ZeroTier has default route. Essential for remote workers routing through corporate network. | S | High — straightforward Nebula `unsafe_routes` with `0.0.0.0/0`. Just config generation + UI toggle. 3-5 days. | #7 |

### Phase 2C: Operator Infrastructure

Features that make hopssh enterprise-credible. Neither DN nor ZeroTier is strong here.

| # | Feature | Description | Why | Size | Viability | Depends on |
|---|---------|-------------|-----|------|-----------|------------|
| 9 | **Log streaming / export** | Export audit logs to S3-compatible storage, or stream to a syslog endpoint. | Tailscale streams to S3, GCS, Datadog, Splunk. Major enterprise purchase driver. Even basic S3 + syslog export is a big step up from competitors. | M | High — audit log data already exists and is batched. Add export worker that reads from audit_log table and ships to configured destination. | #2 |
| 10 | **Terraform provider** | Publish a Terraform provider for networks, nodes, DNS records, and enrollment. | Tailscale and ZeroTier both have official providers. Required for IaC-heavy teams. DN notably lacks this — opportunity to leapfrog. | L | High — API has solid CRUD coverage for networks, nodes, DNS. Minor gap: need PATCH for network updates. Standard Terraform provider SDK work. | #4 |
| 11 | **Session recording** | Record terminal sessions (encrypted, stored locally or in S3). Playback in dashboard. | Tailscale has recorder nodes. Major compliance requirement (SOC 2, PCI). High revenue potential — enterprise gate feature. | M | High — PTY data flows through `ptmx.Read()` → `conn.WriteMessage()`. Intercept with `io.MultiWriter` to tee to storage. Clean interception points in both agent and proxy. ~1-2 weeks. | #9 |
| 12 | **Device approval** | New devices require admin approval before joining the mesh. Configurable per network. | Tailscale has device approval. Prevents unauthorized devices from joining. Required for compliance. Simpler than full OIDC. | M | High — enrollment flow already has pending/authorized states (device_codes table). Extend to hold all enrollments in "pending" until admin approves. | — |
| 13 | **GitOps config export** | Export network config (firewall rules, DNS records, routes) as declarative YAML/JSON. Import to apply. | Tailscale supports gitops for ACLs. Enables version-controlled infrastructure. Pairs with Terraform provider. | M | High — all data is in SQLite, just needs serialization endpoints. | #6 |

### Phase 3A: Network Depth

Features that add network fabric depth. Positions hopssh against ZeroTier's networking strengths.

| # | Feature | Description | Why | Size | Viability | Depends on |
|---|---------|-------------|-----|------|-----------|------------|
| 14 | **Regional relay nodes** | Add standalone relay nodes in different regions via the dashboard. Configure, deploy, and monitor relay health. | ZeroTier has private roots/moons. DN requires customer-hosted relays with no management UI. Dashboard-managed relays are a UX win and needed for scale past ~10K nodes. | L | Moderate — lighthouses are currently tightly coupled to the control plane binary. Need to decouple relay into separate enrollment type and modify Nebula config to reference remote relay addresses. New enrollment flow needed. | — |
| 15 | **Peer connectivity map** | Visual network topology showing P2P vs relayed connections, handshake status, latency. | No competitor has a good version of this. Operators need to see mesh health during incidents. | M | Uncertain — depends on whether Nebula vendor library exposes peer stats API. Agent currently has no stats endpoint. Need to inspect `vendor/slackhq/nebula` for `GetHostMap()` or similar. May require vendor patch. | #14 |
| 16 | **IPv6 overlay** | Support IPv6 addresses in the mesh overlay using Nebula v2 certificates. | ZeroTier and Tailscale both support IPv6. Nebula v2 certs add this natively. Needed for modern infrastructure. | M | High — Nebula v2 certs support IPv6. Requires updating PKI code to use v2 cert format and extending subnet allocation. | — |

### Phase 3B: Identity & Access Platform

Build the identity-aware features that make Tailscale the market leader. Each is independently valuable.

| # | Feature | Description | Why | Size | Viability | Depends on |
|---|---------|-------------|-----|------|-----------|------------|
| 17 | **Policy model (grants-like)** | Unified policy language: "users in group X can reach services tagged Y on ports Z." Compiles to Nebula groups + firewall rules. Policy simulator ("why is this allowed/denied?"). | Tailscale's grants combine network and app-layer permissions. Single most important feature for enterprise sales. | L | Moderate — builds on #6 (firewall rules) + #5 (OIDC). Requires a policy compiler that maps identity claims to Nebula cert groups and generates firewall rules. Design-heavy. | #5, #6 |
| 18 | **App connectors / domain routing** | Route traffic to specific domains (SaaS apps, internal services) through designated mesh nodes. DNS-based routing. | Tailscale app connectors let teams access SaaS apps through the mesh. Moves hopssh from "connectivity" to "access platform." | L | Moderate — extends DNS system + subnet routing. Need DNS interception and per-domain routing rules. | #7, #17 |
| 19 | **Desktop tray app** | Native tray app for macOS and Windows. Connection status, network switching, quick-connect terminal. | All three competitors have desktop apps. Required for non-technical users. | L | Viable but separate project — agent is purely CLI/headless. Recommend Tauri for lightweight cross-platform. Wraps agent CLI + control plane API. 4-8 weeks. | — |
| 20 | **Mobile apps (iOS/Android)** | Native mobile apps to join the mesh. | All three competitors have mobile apps. DN's `mobile_nebula` is MIT-licensed Flutter — consider forking. | L | Viable but separate project — 4-8 weeks per platform. DN's Flutter codebase is a potential starting point. | — |

### Phase 4: Enterprise & Scale

Build when there is customer demand and revenue to justify the investment.

| # | Feature | Description | Why | Size | Viability | Depends on |
|---|---------|-------------|-----|------|-----------|------------|
| 21 | **SAML** | SAML SSO for enterprises that require it (Azure AD, Okta SAML). | Tailscale has SAML. Many enterprises mandate it. OIDC covers most cases. | M | High — standard SAML library integration. | #5 |
| 22 | **SCIM provisioning** | Sync users and groups from IdP (Okta, Azure AD). Auto-create/remove users on group changes. | Tailscale has SCIM. Eliminates manual user management for larger teams. | M | High — standard SCIM endpoint implementation. | #5 |
| 23 | **Workload identity** | Ephemeral nodes authenticate via cloud OIDC tokens (AWS, GCP, GitHub Actions) instead of long-lived keys. | Tailscale has workload identity federation. Essential for CI/CD and Kubernetes. | L | High — extends OIDC to accept platform tokens. | #5 |
| 24 | **RBAC** | Granular permissions beyond admin/member. Custom roles with specific permission sets. | Current admin/member is too coarse for large teams. | L | Moderate — requires permission model redesign. | #17 |
| 25 | **Device posture checks** | Require device health attributes (OS version, disk encryption, antivirus) before granting access. | Tailscale has device posture. Compliance requirement for regulated industries. | L | Moderate — agent needs to report posture attributes; control plane needs to evaluate them against policy. | #12 |
| 26 | **Multi-network per agent** | A single agent joins multiple networks simultaneously. | ZeroTier supports this natively. DN supports via multiple DNClient instances. | L | Low — agent architecture is singleton per enrollment (one config dir, one Nebula instance, one bearer token). Fundamental refactor of agent config, service management, and Nebula instance lifecycle. 4-6 weeks minimum. Consider supporting multiple agent instances as a simpler alternative. | — |
| 27 | **Bandwidth monitoring** | Per-network and per-node traffic metrics. Dashboard graphs. | Useful for capacity planning. No competitor does this well. | M | Moderate — depends on Nebula stats exposure (#15). | #15 |
| 28 | **SOC 2 documentation** | Publish compliance documentation and controls mapping. | Enterprise buyers need this. Not code — a business requirement. | M | N/A — documentation effort, not engineering. | #11 |

---

## Pricing Strategy

### Current model

| Tier | Price | Limits |
|---|---|---|
| Free | $0 | 25 nodes, 1 network |
| Pro | $5/node/month | Unlimited networks, DNS, terminal, audit, API keys |
| Enterprise | Contact | SSO, RBAC, session recording, log streaming, SLA |

### Competitive pricing context

| | Free tier | Paid tier | Model |
|---|---|---|---|
| Defined Networking | 100 hosts | $1/host/month | Per-host. Low price, but customer hosts their own infrastructure. |
| Tailscale | 3 users (personal) | $6/user/month | Per-user. Scales with team size, not device count. |
| ZeroTier | 25 devices | $5/node/month | Per-node. Matches our pricing exactly. |
| hopssh | 25 nodes | $5/node/month | Per-node. All infrastructure bundled. |

### Pricing rationale

**$5/node vs DN's $1/host:** The 5x gap is justified because hopssh bundles lighthouse, relay, DNS, web terminal, health dashboard, and proxy. DN charges $1 but requires customers to host and operate their own lighthouses and relays. Frame as "all-inclusive" vs "assembly required."

**Per-node vs per-user:** Per-node is the right model while our value is infrastructure (mesh, lighthouse, relay, DNS). Consider adding per-user pricing for identity features (SSO, session recording) once Phase 3B ships. A hybrid model (base infrastructure per-node + identity features per-user) could work long-term.

**Free tier at 25 nodes:** Large enough for a real team to evaluate seriously (staging environment, small production). Small enough to convert when they grow. ZeroTier also uses 25.

**Enterprise gates:** SSO, SAML, SCIM, session recording, log streaming, RBAC, and device posture are universally enterprise-tier features across all competitors. Gate these behind Enterprise.

**Self-hosted is free forever.** All features included. This is the core trust differentiator against DN (SaaS only) and Tailscale (SaaS + limited Headscale). Never compromise this.

### Target segments

| Segment | Entry point | Conversion trigger | Revenue potential |
|---|---|---|---|
| Solo devs with VPS | Free tier, blog posts, HN | Grow beyond 25 nodes | Low ($5-50/mo) |
| Small dev teams (2-15) | Word of mouth from solo devs | Team features, audit | Medium ($50-500/mo) |
| Agencies managing client infra | Direct outreach | Client rotation, contractor access | Medium ($100-500/mo) |
| Compliance-bound teams | SOC 2 pain, audit needs | Session recording, SSO, log streaming | High ($500-5,000/mo) |
| DevOps with hybrid/multi-cloud | Terraform provider, IaC | Unified access across environments | High ($500-5,000/mo) |

### Adoption funnel

```
Blog post / HN / tweet / "60 seconds from install to Jellyfin"
        |
Solo dev tries on VPS                              <- FREE (25 nodes)
        |
Uses daily, tells coworkers
        |
Team of 5 uses for staging                         <- FREE (under 25 nodes)
        |
Team grows, adds production (30+ nodes)            <- PRO ($150/mo)
        |
Company needs audit, SSO, compliance               <- ENTERPRISE
```

---

## Scaling Thresholds

### Current architecture (handles ~10,000 nodes)

Single binary: API + web UI + SQLite + lighthouse + relay + DNS.

**SQLite configuration:**
- Driver: `modernc.org/sqlite` (pure Go, no CGO — enables single-binary cross-compilation)
- WAL mode, 10s busy timeout, 32MB page cache, NORMAL sync, 200MB WAL size limit
- Write pool: 1 connection (single writer). Read pool: 20 connections, 5 idle.
- Lock retry: 12-attempt exponential backoff (50ms → 3s, 6.25s total)

**Why this works:**
- Lighthouse memory: ~1KB per node. 10,000 nodes = 10MB.
- Lighthouse bandwidth: ~400KB/s for 10K nodes.
- Relay: only ~8% of connections use relay. Terminal sessions are 2-5 KB/s.
- Hardware: $20-40/month VPS is dramatically overprovisioned.

**Write optimization (implemented):**
- Heartbeat batching: coalesces in `sync.Map`, flushes one transaction every 5s. Reduces write locks from ~4000/min to ~12/min at 1K agents. At 10K nodes with 30s heartbeats, each flush handles ~333 coalesced UPDATEs in one transaction — well within SQLite's capability.
- Audit log batching: buffered channel (1000 capacity), flushes every 2s or every 100 entries. Non-blocking overflow drops entries to keep request latency predictable.
- Lock retry with escalating backoff (PocketBase pattern)
- Connection idle timeout, journal size limit
- Nebula tunnels close properly (vendor patch)

**Roadmap features do not change the scaling picture.** All planned features (webhooks, API keys, SSO, firewall rules, session recording, device approval) add low-frequency write patterns. The two hot write paths (heartbeats and audit) are already batched.

**Why not Turso/libsql?** Evaluated and rejected. libsql inherits SQLite's single-writer model — it adds read replication, not write scaling. The Go driver (`go-libsql`) requires CGO, which breaks our pure-Go single-binary build. The CGO-free driver (`turso-go`) is beta and not production-ready. The server component (`sqld`) is a separate Rust binary, incompatible with single-binary deployment. Turso does not solve the write contention problem.

### ~10,000 nodes: Add regional relays

Standalone Nebula relay nodes in different regions. No app logic — just Nebula config.
Reduces latency for relayed cross-region connections. Control plane stays single server.
Database stays SQLite — write load scales linearly with heartbeat batching.

### ~50,000 nodes: Tune flush intervals + shard by network

- Increase heartbeat flush interval (5s → 15s) and/or reduce agent heartbeat frequency
- WAL autocheckpoint tuning (`PRAGMA wal_autocheckpoint`)
- Shard SQLite by network: separate `.db` file per network. The architecture already isolates networks (per-network CA, lighthouse, DNS). Sharding the database is a natural extension — each network's writes are independent. This eliminates cross-network write contention entirely.

### ~100,000+ nodes: PostgreSQL

Swap SQLite for PostgreSQL. Multiple control plane instances behind load balancer.
Regional lighthouses. Dedicated relay fleet. At this scale, revenue justifies the infrastructure complexity.

| Nodes | Control Plane | Relay | Database | Key change |
|---|---|---|---|---|
| 0–10K | Single binary | Embedded | SQLite (current) | Already handles this |
| 10K–50K | Single binary | Regional relay nodes | SQLite + tuned flush intervals | Config changes only |
| 50K–100K | Single binary | Regional relay fleet | SQLite sharded per network | Moderate refactor |
| 100K+ | Horizontal (load balanced) | Dedicated relay fleet | PostgreSQL | Major architecture change |

---

## Launch Checklist

- [x] Phase 1 networking complete (mesh + DNS + terminal + enrollment)
- [x] Agent binary on GitHub Releases (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64, windows/arm64)
- [x] CI/CD pipeline (build, test, cross-compile, Docker multi-arch, release automation)
- [x] Docker support (distroless, multi-arch, compose)
- [x] Install script served by control plane
- [x] Self-update for agent and server
- [ ] Deployed on a VPS with public IP and TLS
- [ ] Domain: hopssh.com with HTTPS
- [ ] Landing page with sign-up and self-hosted download
- [ ] Demo video: "60 seconds from install to Jellyfin"
- [ ] Blog post: "Why we built hopssh"
- [ ] Documentation site
- [ ] Phase 2A features (#1-#6) — beat Defined Networking

---

## Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Tailscale adds browser terminal | Medium | High | Move fast, self-hosted moat, community |
| DN launches self-hosted option | Low | High | We're further ahead on features (terminal, DNS, proxy) |
| Security incident with agent | Low | Critical | Minimal surface, no stored creds, short-lived certs, bug bounty |
| Low free→paid conversion | High | Medium | Generous free tier drives adoption, team/compliance features drive conversion |
| $5/node perceived as expensive vs DN $1 | Medium | Medium | Communicate bundled value (infra, terminal, DNS, proxy) |
| Nebula upstream breaking change | Low | Medium | Vendor + patch, monitor releases, engage with upstream |
