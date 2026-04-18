# hopssh — Product Roadmap

*Last updated: 2026-04-15*

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
- macOS batch syscalls (`sendmsg_x`/`recvmsg_x`) — 17% → 35-53% tunnel efficiency
- macOS TUN batch reads, control-lane priority queue, TUN buffer caching, AES-GCM default

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
- No connection visibility — P2P vs relay status not shown (the #1 cross-competitor pain point, and we don't have it either)
- No SSO/OIDC (DN, Tailscale, ZeroTier all have it — table stakes for teams)
- No scoped API keys (DN and Tailscale have them)
- No granular firewall rules (DN has roles+tags, Tailscale has ACLs+grants, ZeroTier has flow rules)
- No mobile or desktop apps (all three competitors have them — mobile is a top-3 complaint for ZT and DN)
- No webhooks, log streaming, or Terraform provider (Tailscale leads here; DN lacks Terraform — opportunity)
- No subnet routing or exit nodes (Tailscale and ZeroTier have them — big homelab use case)
- No session recording (Tailscale has it — enterprise gate)
- No connection diagnostics ("why is this relayed?" — no competitor answers this well either)

---

## Implementation Roadmap

Every feature is numbered and ordered by impact — highest value to the product first. Each entry has a complexity estimate (S = days, M = 1-2 weeks, L = 2-4 weeks), a viability assessment based on codebase review, and a "why" tied to competitive analysis or market need.

**Go-to-market strategy: selfhosters first, corporate second.** Infrastructure tools win bottom-up: developer tries at home → loves it → brings to work → company pays. Docker, Tailscale, Cloudflare all followed this path. Phase 2A and 2B focus on making selfhosters so happy they become our sales team. Enterprise features (Phase 2C+) are the monetization layer on top of organic adoption.

### Phase 2A: Delight Selfhosters

Ship the features selfhosters are actively asking for across competitor communities. These directly address the top pain points from cross-competitor research (2026-04-15) — connection visibility, networking, and UX. Every item here has a direct mapping to a real user complaint about ZeroTier, Tailscale, or Defined Networking.

| # | Feature | Description | Why | Size | Viability | Depends on |
|---|---------|-------------|-----|------|-----------|------------|
| 1 | **Expand free tier to 25 nodes** | Increase from 10 to 25 nodes on the free tier. | Our 10-node free tier is the smallest in market. DN offers 100, ZeroTier offers 25. ZT's reduction from 100→25 caused massive backlash — shows free tier matters to selfhosters. 25 is enough for serious evaluation without killing conversion. | S | Trivial — config change only | — |
| 2 | **P2P/relay status per node** ✅ v0.9.10 | Show connection type (P2P, relayed, offline) per node in the dashboard. Agent reports peer counts in heartbeat; server derives a `connectivity` string (direct/mixed/relayed/idle); dashboard shows a colored badge per node. | **#1 ZeroTier complaint by volume.** Users can't tell if connections are P2P or relayed. No competitor shows this in the dashboard — Tailscale requires CLI, ZeroTier requires `zerotier-cli peers` (cryptic output). Selfhosters obsess over their network health. | S | **Shipped** — `cmd/agent/peerstate.go`, migration 002, `internal/api/types.go:deriveConnectivity`, dashboard badge + tooltip. | — |
| 3 | **GitHub OAuth** | Add GitHub as a login provider. OAuth redirect + callback handler, user lookup/creation by `github_id`. | Selfhosters are developers. GitHub login removes all friction from the first experience. Tailscale and ZeroTier have it. | S | High — `github_id` column already exists in `users` table, session creation path is reusable. ~200-300 LOC. | — |
| 4 | **Connection diagnostics** | "Diagnose" action per node in dashboard: connection type, latency, NAT type, handshake age, packet loss. Agent exposes a `/stats` endpoint queried on-demand. | "Why is my connection relayed?" is the most common support question across ZT, TS, and DN. No competitor answers it well. Selfhosters want to understand their network, not just use it. One-click dashboard action beats CLI-only investigation. | M | Moderate — Nebula's host map has peer state. Need vendor inspection for `GetHostMap()` or equivalent. Agent stats endpoint + dashboard UI. ~400-600 LOC. | #2 |
| 5 | **Subnet routing** | Allow designated nodes to route traffic to non-overlay subnets (LAN, cloud VPCs). Dashboard UI to configure routes per node. | **THE homelab gateway feature.** "Access my LAN through the mesh" is the #1 reason selfhosters adopt a mesh VPN. TS has it but it's flaky (their #6 complaint — failover, MTU issues). ZT has managed routes. DN has it. Opportunity to do it right. | M | High — Nebula supports `unsafe_routes` natively, just not exposed. Add `routes` column to networks, generate route config in agent Nebula template. Note: routing node must run as root. ~300-500 LOC. | — |
| 6 | **Exit nodes** | Designate a node as an exit node to route all traffic (or specific domains) through the mesh. | "Route all my traffic through my home server" — the second most common homelab use case after LAN access. Tailscale has it, ZeroTier has default route. Essential for selfhosters traveling or on untrusted WiFi. | S | High — straightforward Nebula `unsafe_routes` with `0.0.0.0/0`. Just config generation + UI toggle. 3-5 days. | #5 |
| 7 | **Bulk node operations** | Select multiple nodes → authorize, delete, change capabilities, move to group. Batch API endpoints. | ZeroTier users complain about authorizing nodes one-by-one. Tailscale admin console lacks bulk operations. Any selfhoster with 10+ nodes (Pi, NAS, VPS, etc.) needs this. | S | High — existing API handlers process single nodes. Add batch wrappers + multi-select UI. ~200-400 LOC. | — |

### Phase 2B: Expand Networking & Access Control

Deepen the networking capabilities and add the access control that power selfhosters and small teams need. These features move hopssh from "mesh VPN" to "network platform."

| # | Feature | Description | Why | Size | Viability | Depends on |
|---|---------|-------------|-----|------|-----------|------------|
| 8 | **Granular firewall rules** | Tag-based firewall: assign tags (key:value) to nodes, define rules between tag groups on specific ports/protocols. Compile to Nebula cert groups + firewall config. UI for rule management with preview. | DN has roles + tags + firewall builder. TS has ACLs (HuJSON — users complain it's complex). ZT has flow rules. Current Nebula config is wide-open allow-all. A visual rule builder beats TS's JSON editing (a common TS complaint). Selfhosters with mixed-trust networks (media server vs NAS vs public-facing) need this. | L | Moderate — Nebula supports group-based firewall natively. Currently certs are issued with hardcoded `groups: ["node"]`. Need: tags table, rule generation, cert re-issuance. Caveat: tag changes require cert renewal (24h cycle, or add immediate renewal endpoint). ~400-600 LOC. | — |
| 9 | **Webhooks** | Send HTTP webhooks on events (node online/offline, enrollment, member changes, audit events). Configurable per network with retry and HMAC signing. | Tailscale and ZeroTier both have webhooks. Selfhosters love automation — Slack alerts when a node goes down, Home Assistant integration. The internal `EventHub` pub/sub system is already built and publishing 8+ event types — webhooks just add an HTTP delivery layer on top. | M | Very high — event system is 90% done. Add `network_webhooks` table, subscribe to EventHub, POST with retry. ~400-600 LOC. | — |
| 10 | **Scoped API keys** | Implement API key creation, listing, deletion, and scoped permissions. Add `scopes` column to existing `api_keys` table. Auth middleware to validate keys alongside sessions. | DN has scoped API keys with OpenAPI spec. Selfhosters who automate everything need API access. Unblocks Terraform provider (#14). | M | Moderate — `api_keys` table exists but needs `scopes` column. Need new middleware alongside existing `RequireAuth`. ~500-800 LOC. | — |
| 11 | **Device approval** | New devices require admin approval before joining the mesh. Configurable per network. | Tailscale has device approval. Selfhosters sharing networks with family/friends want to control who joins. Required for compliance in team settings. Simpler than full OIDC. | M | High — enrollment flow already has pending/authorized states (device_codes table). Extend to hold all enrollments in "pending" until admin approves. | — |

### Phase 2C: Enterprise Gates

Features that make hopssh enterprise-credible. These are the monetization layer — ship them when selfhoster adoption creates inbound enterprise interest. The signal to start: "We're already using hopssh for our homelab, can we get SSO for the team?"

| # | Feature | Description | Why | Size | Viability | Depends on |
|---|---------|-------------|-----|------|-----------|------------|
| 12 | **SSO/OIDC** | Add OIDC login (Google, Microsoft, Okta, custom). Standard OpenID Connect discovery + token exchange. | DN, Tailscale, and ZeroTier all have SSO. The gatekeeper for enterprise conversations. Opens the door to all identity-aware features. Research confirms: DN users cite missing SSO as a top complaint; TS users consider it table stakes. | L | Moderate — no OIDC code exists today. Session system is simple enough to extend (create user from ID token claims, reuse session creation). Needs `golang.org/x/oauth2` dependency. ~800-1200 LOC. | — |
| 13 | **Log streaming / export** | Export audit logs to S3-compatible storage, or stream to a syslog endpoint. | Tailscale streams to S3, GCS, Datadog, Splunk. Major enterprise purchase driver. Even basic S3 + syslog export is a big step up from competitors. | M | High — audit log data already exists and is batched. Add export worker that reads from audit_log table and ships to configured destination. | #9 |
| 14 | **Terraform provider** | Publish a Terraform provider for networks, nodes, DNS records, and enrollment. | Tailscale and ZeroTier both have official providers. Required for IaC-heavy teams. DN notably lacks this — opportunity to leapfrog. Research: DN's lack of Terraform is cited as a gap; small Nebula ecosystem is a common complaint. | L | High — API has solid CRUD coverage for networks, nodes, DNS. Minor gap: need PATCH for network updates. Standard Terraform provider SDK work. | #10 |
| 15 | **Session recording** | Record terminal sessions (encrypted, stored locally or in S3). Playback in dashboard. | Tailscale has recorder nodes. Major compliance requirement (SOC 2, PCI). High revenue potential — enterprise gate feature. | M | High — PTY data flows through `ptmx.Read()` → `conn.WriteMessage()`. Intercept with `io.MultiWriter` to tee to storage. Clean interception points in both agent and proxy. ~1-2 weeks. | #13 |
| 16 | **GitOps config export** | Export network config (firewall rules, DNS records, routes) as declarative YAML/JSON. Import to apply. | Tailscale supports gitops for ACLs. Enables version-controlled infrastructure. Pairs with Terraform provider. | M | High — all data is in SQLite, just needs serialization endpoints. | #8 |

### Networking Priorities (Cross-Phase)

These don't fit neatly into the feature phases — they're transport-level improvements that affect all users.

| # | Feature | Description | Why | Size | Status |
|---|---------|-------------|-----|------|--------|
| N1 | **TCP/443 relay fallback** | WebSocket relay through the control plane's HTTPS port (9473, already open). Agent detects UDP relay failure → connects via WebSocket. | Some networks block UDP entirely (corporate firewalls, hotel WiFi). Tailscale solves this with DERP (TCP/443). Universal connectivity through any network. | L (~500-1000 LOC) | Planned |
| N2 | **Adaptive connection quality** | Detect P2P vs relay, measure RTT, expose to dashboard. Auto-tune keepalive intervals based on measured path quality. | Operators need to see mesh health. No competitor has a good version of this. | M | Planned |
| N3 | **P2P on symmetric NAT** | Port prediction or alternative hole-punching for carrier-grade NAT with random port assignment. | Symmetric NAT (most mobile carriers) fails P2P → relay fallback. | — | **Not viable.** Port prediction tried and reverted — carriers use random ports. This is unsolved industry-wide for random-port symmetric NAT. Relay works (125ms avg, only 9ms overhead). |

### Phase 3A: Network Depth

Features that add network fabric depth. Positions hopssh against ZeroTier's networking strengths.

| # | Feature | Description | Why | Size | Viability | Depends on |
|---|---------|-------------|-----|------|-----------|------------|
| 17 | **Regional relay nodes** | Add standalone relay nodes in different regions via the dashboard. Configure, deploy, and monitor relay health. | ZeroTier has private roots/moons. DN requires customer-hosted relays with no management UI. TS DERP servers are concentrated in US/EU — users in Asia/Africa/South America report high relay latency. Dashboard-managed relays are a UX win and needed for scale past ~10K nodes. | L | Moderate — lighthouses are currently tightly coupled to the control plane binary. Need to decouple relay into separate enrollment type and modify Nebula config to reference remote relay addresses. New enrollment flow needed. | — |
| 18 | **Peer connectivity map** | Visual network topology showing P2P vs relayed connections, handshake status, latency per peer. Full mesh visualization. | Builds on #2 (per-node status) to show the complete network graph. No competitor has this. Operators need to see mesh health during incidents. | M | High — #2 and #4 establish the agent stats foundation. This is the dashboard visualization layer on top. | #2, #4 |
| 19 | **IPv6 overlay** | Support IPv6 addresses in the mesh overlay using Nebula v2 certificates. | ZeroTier and Tailscale both support IPv6. Nebula v2 certs add this natively. Needed for modern infrastructure. | M | High — Nebula v2 certs support IPv6. Requires updating PKI code to use v2 cert format and extending subnet allocation. | — |
| N4 | **macOS batch UDP via `sendmsg_x`/`recvmsg_x`** | Pure Go (no CGO) implementation of XNU private batch syscalls (#481/#480). Vendor patches 04-08 in `patches/`. | **No other VPN uses these.** Tunnel efficiency 17% → 35-53% of raw WiFi. First mesh VPN with batch UDP on macOS. | M | — | ✅ Done (patches 04-08). Pure Go, not CGO. |

### Phase 3B: Identity & Access Platform

Build the identity-aware features that make Tailscale the market leader. Each is independently valuable.

| # | Feature | Description | Why | Size | Viability | Depends on |
|---|---------|-------------|-----|------|-----------|------------|
| 20 | **Policy model (grants-like)** | Unified policy language: "users in group X can reach services tagged Y on ports Z." Compiles to Nebula groups + firewall rules. Policy simulator ("why is this allowed/denied?"). | Tailscale's grants combine network and app-layer permissions. Research: TS ACL complexity is a top complaint — a visual policy builder is a clear win. Single most important feature for enterprise sales. | L | Moderate — builds on #8 (firewall rules) + #12 (OIDC). Requires a policy compiler that maps identity claims to Nebula cert groups and generates firewall rules. Design-heavy. | #8, #12 |
| 21 | **App connectors / domain routing** | Route traffic to specific domains (SaaS apps, internal services) through designated mesh nodes. DNS-based routing. | Tailscale app connectors let teams access SaaS apps through the mesh. Moves hopssh from "connectivity" to "access platform." | L | Moderate — extends DNS system + subnet routing. Need DNS interception and per-domain routing rules. | #5, #20 |
| 22 | **Desktop tray app** | Native tray app for macOS and Windows. Connection status, network switching, quick-connect terminal. | All three competitors have desktop apps. Required for non-technical users. Research: Linux users complain about no GUI on TS; macOS TS app has quirks. | L | Viable but separate project — agent is purely CLI/headless. Recommend Tauri for lightweight cross-platform. Wraps agent CLI + control plane API. 4-8 weeks. | — |
| 23 | **Mobile apps (iOS/Android)** | Native mobile apps to join the mesh. | All three competitors have mobile apps. Research: mobile is a top-3 complaint for ZT (unreliable) and DN (unstable Flutter). DN's `mobile_nebula` is MIT-licensed Flutter — consider forking but build native for reliability. | L | Viable but separate project — 4-8 weeks per platform. Avoid Flutter (DN's choice, cited as "clunky and crashy"). | — |

### Phase 4: Enterprise & Scale

Build when there is customer demand and revenue to justify the investment.

| # | Feature | Description | Why | Size | Viability | Depends on |
|---|---------|-------------|-----|------|-----------|------------|
| 24 | **SAML** | SAML SSO for enterprises that require it (Azure AD, Okta SAML). | Tailscale has SAML. Many enterprises mandate it. OIDC covers most cases. | M | High — standard SAML library integration. | #12 |
| 25 | **SCIM provisioning** | Sync users and groups from IdP (Okta, Azure AD). Auto-create/remove users on group changes. | Tailscale has SCIM. Eliminates manual user management for larger teams. | M | High — standard SCIM endpoint implementation. | #12 |
| 26 | **Workload identity** | Ephemeral nodes authenticate via cloud OIDC tokens (AWS, GCP, GitHub Actions) instead of long-lived keys. | Tailscale has workload identity federation. Essential for CI/CD and Kubernetes. | L | High — extends OIDC to accept platform tokens. | #12 |
| 27 | **RBAC** | Granular permissions beyond admin/member. Custom roles with specific permission sets. | Current admin/member is too coarse for large teams. | L | Moderate — requires permission model redesign. | #20 |
| 28 | **Device posture checks** | Require device health attributes (OS version, disk encryption, antivirus) before granting access. | Tailscale has device posture. Compliance requirement for regulated industries. | L | Moderate — agent needs to report posture attributes; control plane needs to evaluate them against policy. | #11 |
| 29 | **Multi-network per agent** | A single agent joins multiple networks simultaneously. | ZeroTier supports this natively. DN supports via multiple DNClient instances. Research: TS users complain about no multi-account support. | L | Low — agent architecture is singleton per enrollment (one config dir, one Nebula instance, one bearer token). Fundamental refactor of agent config, service management, and Nebula instance lifecycle. 4-6 weeks minimum. Consider supporting multiple agent instances as a simpler alternative. | — |
| 30 | **Bandwidth monitoring** | Per-network and per-node traffic metrics. Dashboard graphs. | Useful for capacity planning. No competitor does this well. Research: no competitor offers this — genuine whitespace. | M | Moderate — depends on Nebula stats exposure (#18). | #18 |
| 31 | **SOC 2 documentation** | Publish compliance documentation and controls mapping. | Enterprise buyers need this. Not code — a business requirement. | M | N/A — documentation effort, not engineering. | #15 |

---

## Pricing Strategy

### Current model

| Tier | Price | Limits |
|---|---|---|
| Free | $0 | No limit enforced today — target: 25 nodes, 1 network |
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

### Adoption funnel (selfhosters → corporate)

```
Blog post / HN / r/selfhosted / "60 seconds from install to Jellyfin"
        |
Solo selfhoster tries on VPS + homelab             <- FREE (25 nodes)
        |
Loves it: DNS works, sees P2P vs relay, subnet routes to LAN
        |
Writes blog post / Reddit comment / tells coworkers
        |
Coworker's team tries for staging                  <- FREE (under 25 nodes)
        |
Team grows, adds production (30+ nodes)            <- PRO ($150/mo)
        |
Company needs SSO, session recording, compliance   <- ENTERPRISE
```

The key insight: Phase 2A features (connection visibility, subnet routing, exit nodes) are what turn a "it works" experience into a "this is amazing, you should try it" recommendation. Enterprise features (Phase 2C) monetize the adoption that selfhoster delight creates.

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
- [x] Deployed on a VPS with public IP and TLS (OCI arm64, Nomad + Traefik, v0.9.5)
- [x] Domain: hopssh.com with HTTPS (Let's Encrypt via Traefik)
- [ ] Landing page with sign-up and self-hosted download
- [ ] Demo video: "60 seconds from install to Jellyfin"
- [ ] Blog post: "Why we built hopssh"
- [ ] Documentation site
- [ ] Phase 2A features (#1-#7) — quick wins + foundation

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
