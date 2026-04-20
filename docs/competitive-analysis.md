# hopssh — Competitive Analysis

*hopssh vs Defined Networking vs Tailscale vs ZeroTier*

*Last updated: 2026-04-15*

---

## Feature Matrix

|  | hopssh | Defined Networking | Tailscale | ZeroTier |
|---|---|---|---|---|
| **Deployment** | | | | |
| Self-hosted control plane | Yes (single binary) | No (SaaS only) | No (SaaS); Headscale (community) | Yes (self-hosted controller) |
| Single binary | Yes | No | Yes (client only) | No |
| Bundled lighthouse/relay | Yes (embedded) | No (customer-hosted) | Yes (DERP relays) | Yes (roots/moons) |
| Docker support | Yes (distroless, multi-arch) | DNClient in Docker | Yes | Yes |
| | | | | |
| **Protocol & Encryption** | | | | |
| Overlay protocol | Nebula (Noise, Curve25519) | Nebula | WireGuard | Custom (ChaCha20-Poly1305) |
| E2E encryption | Yes | Yes | Yes | Yes |
| P2P with hole punching | Yes (Nebula punchy) | Yes | Yes | Yes |
| NAT-PMP / UPnP port mapping | **Yes (v0.10.3+, RFC 6886)** — own implementation, zero deps | No | Yes (`net/portmapper`) | No |
| P2P across home-router + cellular CGNAT | **Yes (v0.10.3+)** — empirically verified, 35-43 ms RTT | No (relay only) | Yes | No (relay only) |
| Relay fallback | Yes (lighthouse relay, UDP) | Yes (customer-hosted relays) | Yes (DERP, TCP/443) | Yes (roots, TCP) |
| Per-network CA / crypto isolation | Yes (separate CA per network) | Yes | No (single tailnet) | No (single network key) |
| Adaptive MTU (DPLPMTUD) | No (planned) | No | No (experimental) | No |
| macOS batch syscalls (sendmsg_x) | **Yes — unique** | No | No | No |
| | | | | |
| **Identity & Auth** | | | | |
| Email/password auth | Yes | No (SSO only for panel) | No (SSO only) | Yes |
| SSO / OIDC | No | Yes | Yes (Google, Microsoft, Okta, etc.) | Yes |
| SAML | No | No | Yes | No |
| SCIM provisioning | No | No | Yes | No |
| GitHub OAuth | No (schema ready) | No | Yes | Yes |
| Device approval | No | No | Yes | No |
| Device posture checks | No | No | Yes | No |
| Tailnet Lock / trust reduction | No | No | Yes | No |
| MFA / 2FA | No | Via IdP | Via IdP | No |
| | | | | |
| **Access Control** | | | | |
| Role-based access | Admin / Member | Roles + Tags (granular) | ACL policies (HuJSON) | Flow rules |
| Firewall rules UI | Capabilities only | Roles + Tags + Firewall builder | ACL editor + grants | Flow rule editor |
| Grants / unified policy | No | No | Yes (network + app layer) | No |
| Per-node capabilities | Yes (terminal, health, forward) | No | No | No |
| Network segmentation | Per-network isolation (separate CA) | Per-network isolation | Tags + ACLs (single tailnet) | Multiple networks per device |
| | | | | |
| **Networking** | | | | |
| Multi-network support | Yes | Yes | Single tailnet model | Yes (multiple per device) |
| Subnet routing | No | Yes (2 free, 100 pro) | Yes (subnet routers) | Yes (managed routes) |
| Exit nodes | No | No | Yes | No |
| Split DNS | Yes (per-network domain) | No | Yes (MagicDNS) | Partial (DNS server push) |
| Custom DNS records | Yes | No | Yes (via MagicDNS) | No |
| User-defined domains | Yes (.hop, .prod, .lab) | No | No (.ts.net only) | No |
| App connectors / domain routing | No | No | Yes | No |
| L2 bridging | No | No | No | Yes |
| Multipath bonding | No | No | No | Yes |
| IPv6 overlay | No | Yes (Nebula v2 certs) | Yes | Yes |
| | | | | |
| **Management & UX** | | | | |
| Web dashboard | Yes (embedded SPA) | Yes (admin.defined.net) | Yes (admin console) | Yes (my.zerotier.com) |
| Web terminal (browser SSH) | Yes | No | Yes (SSH Console, limited) | No |
| Port forwarding via dashboard | Yes (TCP + HTTP proxy) | No | No (Tailscale Serve/Funnel) | No |
| Real-time health dashboard | Yes (WebSocket) | No | Yes | Partial |
| Node rename | Yes (auto-updates DNS) | Yes | Yes | Yes |
| Connectivity/topology map | **Yes (v0.9.13, cytoscape)** | No | No | No |
| Serve / Funnel (expose to internet) | No | No | Yes | No |
| | | | | |
| **Audit & Compliance** | | | | |
| Audit logging | Yes | Yes | Yes | Yes |
| Session recording | No | No | Yes | No |
| Log streaming / SIEM export | No | No (CSV export only) | Yes (S3, GCS, Datadog, etc.) | No |
| Webhooks | No | No | Yes | Yes |
| Network flow logs | No | No | Yes | No |
| | | | | |
| **API & Automation** | | | | |
| REST API | Yes (full CRUD) | Yes (OpenAPI, scoped keys) | Yes | Yes |
| API key scoping | No (schema ready) | Yes | Yes | Yes |
| Terraform provider | No | No | Yes (official) | Yes (official) |
| Pulumi provider | No | No | Yes | No |
| GitOps / config export | No | No | Yes (gitops for ACLs) | No |
| Webhooks for events | No | No | Yes | Yes |
| | | | | |
| **Clients & Platforms** | | | | |
| Linux agent | Yes | Yes | Yes | Yes |
| macOS agent | Yes | Yes | Yes | Yes |
| Windows agent | Yes | Yes | Yes | Yes |
| iOS app | No | Yes | Yes | Yes |
| Android app | No | Yes | Yes | Yes |
| Desktop tray app | No | Yes (Wails) | Yes | Yes |
| FreeBSD | No | Yes | Yes | Yes |
| Broad arch (MIPS, RISC-V, PPC) | No | Yes (12+) | Partial | Yes |
| | | | | |
| **Enrollment** | | | | |
| Device flow (RFC 8628) | Yes | No | No | No |
| Token-based | Yes (one-time, 10min) | Yes (enrollment codes) | Yes (auth keys) | No |
| Bundle / offline | Yes (air-gapped tarball) | No | No | No |
| SSO-based enrollment | No | Yes | Yes (auto via IdP) | No |
| | | | | |
| **Operations** | | | | |
| Self-update (agent) | Yes | Yes | Yes | Yes |
| Self-update (server) | Yes | N/A (SaaS) | N/A (SaaS) | Yes |
| Install script | Yes (dynamic, endpoint pre-baked) | No | Yes | Yes |
| Non-root / userspace mode | Yes | Yes (userspace Nebula) | Yes | Yes |
| Short-lived certificates | Yes (24h, auto-renew) | Yes (auto-renew) | Yes (WireGuard key rotation) | No (persistent keys) |
| | | | | |
| **Pricing** | | | | |
| Free tier | 10 nodes, 1 network | 100 hosts, 2 routes | 3 users (personal) | 25 devices |
| Paid | $5/node/month | $1/host/month | $6/user/month | $5/node/month (commercial) |
| Self-hosted cost | Free (all features) | N/A | Free (Headscale, limited) | Free (controller) |
| Open source | MIT (Nebula core) | MIT (Nebula core) | BSD (client only) | BSL (core) |

---

## Per-Competitor Analysis

### Defined Networking

**What they are:** The commercial company behind Nebula. Founded in 2020 by Nebula's creators (Nate Brown, Ryan Huber) after building it at Slack for 50,000+ hosts. They sell Managed Nebula — a SaaS management plane that handles PKI, enrollment, config distribution, and audit logging. The data plane (Nebula itself) runs entirely on customer infrastructure.

**What they have that we don't:**
- SSO/OIDC for host enrollment
- Granular firewall rules with roles + tags (key:value)
- Scoped API keys (OpenAPI spec)
- Mobile apps (iOS/Android — Flutter, open source)
- Desktop tray app (macOS/Windows — Wails framework)
- Subnet routing (2 free, 100 pro)
- Broad architecture support (12+ including MIPS, RISC-V, PPC64LE)
- 100-host free tier (vs our 10)
- 6 years of production maturity

**What we have that they don't:**
- Self-hosted control plane (their biggest gap — SaaS only, no sovereignty)
- Bundled lighthouse + relay (they require customer-hosted infrastructure)
- Web terminal (browser-based PTY through the mesh)
- Built-in DNS with user-defined domains
- Port forwarding and HTTP proxy through the dashboard
- Real-time health dashboard (WebSocket-driven)
- Single binary deployment (zero infrastructure)
- 4 enrollment modes including device flow and offline bundles

**How they make money:** $1/host/month (Pro), custom enterprise pricing. Hosts, lighthouses, and relays all count as billable hosts. Key revenue insight: they charge per-host, not per-user. The free tier is generous (100 hosts) to drive adoption, then they monetize on scale and support. Their infrastructure costs are low because they don't host lighthouses or relays — customers do.

**Key takeaway:** Defined is the easiest competitor to beat on product completeness. Their value is in managed Nebula, but they don't host the infrastructure, don't have a web terminal, don't have DNS, and don't offer self-hosting. Our biggest gap is identity (SSO), firewall granularity (roles+tags), and client breadth (mobile/desktop). The $1 vs $5 per node pricing gap needs a clear justification — our bundled infrastructure, terminal, DNS, and proxy features are that justification.

---

### Tailscale

**What they are:** The market leader in modern mesh VPN. Founded in 2019, built on WireGuard. Tailscale has evolved from "easy WireGuard mesh" into an identity-aware private access platform with grants, device posture, app connectors, SSH, session recording, and log streaming. They are the benchmark for product completeness and developer UX.

**What they have that we don't:**
- SSO/OIDC/SAML with every major IdP
- SCIM user/group provisioning
- Device approval and posture checks
- Tailnet Lock (trust reduction — nodes sign each other's keys)
- Grants (unified network + application policy model)
- ACL policies in HuJSON with preview/test tooling
- MagicDNS (automatic DNS for all devices, HTTPS certs)
- App connectors (route SaaS domains through the mesh)
- Tailscale Serve / Funnel (expose services to mesh or internet)
- Tailscale SSH (SSH via mesh identity, no SSH keys)
- Session recording (encrypted, customer-managed storage)
- Network flow logs
- Log streaming to S3, GCS, Datadog, Splunk
- Webhooks for all events
- Terraform and Pulumi providers
- GitOps for ACL management
- Workload identity federation (OIDC tokens from CI/cloud)
- iOS, Android, macOS, Windows, Linux clients with tray apps
- SSH Console (limited browser-based SSH)

**What we have that they don't:**
- Self-hosted control plane (Tailscale is SaaS; Headscale is community and limited)
- Per-network cryptographic isolation (separate CA per network vs single tailnet)
- Full browser terminal (not limited SSH Console — real PTY with xterm-256color)
- HTTP proxy to node-local services through the dashboard
- User-defined DNS domains (.hop, .prod — not locked to .ts.net)
- Custom DNS records per network
- Per-node capability toggles (enable/disable terminal, health, forward)
- Bundle enrollment for air-gapped / offline environments
- Device flow enrollment (RFC 8628)
- Single binary server (API + UI + lighthouse + relay + DNS + DB all in one)
- Truly free self-hosted (all features, no limits)

**How they make money:** $6/user/month (Teams), $18/user/month (Business), custom Enterprise. Key insight: they charge per-user, not per-device. This scales better for teams with many devices per person. Premium features are the draw: SSO, device posture, session recording, log streaming, grants. Their free personal plan (3 users) creates a massive developer adoption funnel.

**Key takeaway:** Tailscale is not beatable on breadth in the near term. Their moat is the identity-aware access platform (grants, posture, SCIM, session recording, log streaming) — not just the mesh. The path to competing is: self-hosted trust + open source + Nebula's stronger crypto isolation, combined with progressively closing the identity/policy/observability gap. Beat them where sovereignty matters.

Their [March 2026 product update](https://tailscale.com/blog/march-26-product-update)
shows they are moving *upward* into enterprise AI governance (Aperture AI gateway),
workload identity federation, peer relays, and services. That leaves the "simple
self-hosted mesh for small teams and home-labbers" market lane partly vacated —
which is precisely where NetBird and hopssh can run.

---

### NetBird

**What they are:** The emerging #1 competitor in the self-hosted mesh VPN space
(~24k GitHub stars — second to Headscale (~35k) in the self-hosted VPN space, ~3× Firezone). Built on WireGuard.
Founded as a fully open-source answer to Tailscale's SaaS-coordinated model.
Positioned by most 2026 comparison reviews
([XDA](https://www.xda-developers.com/switched-from-tailscale-to-fully-self-hosted-alternative-netbird/),
[Pinggy](https://pinggy.io/blog/top_open_source_tailscale_alternatives/),
[birdhost](https://birdhost.io/blog/netbird-vs-tailscale)) as the "best self-hosted
Tailscale alternative." Moving fast: v0.63 (January 2026) shipped Custom DNS Zones,
v0.65 (February 2026) shipped a built-in reverse proxy with custom domains and a
unified server binary. This is the competitor to watch.

**What they have that we don't:**
- SSO + MFA with major IdPs in the *free* plan (Authentik, Keycloak, Okta, Azure AD, Google)
- SCIM provisioning (shipped late 2025)
- Custom DNS zones with per-peer-group distribution (v0.63, Jan 2026)
- Built-in reverse proxy with custom domains + authentication (v0.65, Feb 2026)
- iOS and Android mobile apps
- Desktop tray apps (macOS, Windows, Linux)
- Granular access policies (groups, tags, rules) with a dashboard editor
- Multiple identity providers per network
- Subnet routing and egress/exit-node-like features
- Self-hosted everything (control, signal, relay, TURN — every component open-source)

**What we have that they don't:**
- **Per-network cryptographic isolation** — Nebula's separate-CA-per-network model
  is structurally stronger than NetBird's single-WireGuard-tenant architecture.
  NetBird can't easily add this without a significant re-architecture.
- **Single-binary deployment with embedded SPA.** NetBird's "Unified Server Binary"
  (v0.65) combines management + signal + relay into one container, but the default
  deployment still uses Traefik + a separate dashboard container. hopssh's single
  binary (API + UI + lighthouse + relay + DNS + DB) is tighter.
- **Full browser PTY terminal** with real xterm-256color. NetBird's reverse proxy
  exposes services to a browser; it is not a remote-shell product.
- **Per-node capability toggles** (enable/disable terminal, health, forward per node).
- **Device flow enrollment (RFC 8628)** and bundle enrollment for air-gapped environments.

**What we both have (parity, not differentiator):**
- User-defined DNS domains (NetBird shipped this Jan 2026 — no longer hopssh-unique)
- Self-hosted control plane with web UI
- WireGuard or Nebula data plane with hardware AEAD

**How they make money:** Open-core. Self-hosted is free and full-featured. Cloud
tiers (Team, Business) for managed hosting, advanced IdP integrations, and SLAs.
Pricing is per-user, roughly half of Tailscale's per-user equivalent.

**Key takeaway:** NetBird — not Defined Networking — is the competitor whose
lunch hopssh most directly overlaps. They are moving faster than Tailscale on
self-hosted mesh features, with a 2-3 year head start on SSO, mobile, and desktop
clients. We will not out-feature them on identity/policy in 2026; we beat them
on (a) Nebula-based crypto isolation, (b) single-binary elegance, (c) browser
terminal as a native primitive, and (d) the universal resilience gaps nobody has
closed (see "Universal Unsolved Gaps" section below).

---

### ZeroTier

**What they are:** The original modern overlay network. Founded in 2015, built on a custom protocol with Curve25519. ZeroTier is the most network-engineer-friendly product: self-hosted controllers, multiple networks per device, L2 bridging, flow rules, multipath, and physical-network routing. Their DNA is "virtual networking fabric" rather than "access platform."

**What they have that we don't:**
- Self-hosted controllers and private root servers
- Multiple networks per device (join N overlays simultaneously)
- Flow rules (microsegmentation, though "tricky to build" per their docs)
- Subnet routing / managed routes
- L2 Ethernet bridging
- Multipath bonding (link aggregation)
- TCP relay fallback (roots)
- Terraform provider
- Webhooks
- iOS, Android, macOS, Windows, Linux, FreeBSD clients
- 25-device free tier
- BSL license (source-available core)
- IPv6 overlay

**What we have that they don't:**
- Web terminal (browser SSH through the mesh)
- Built-in DNS with user-defined domains (ZeroTier's DNS docs say managed DNS still doesn't let you address hosts by controller-defined names)
- HTTP proxy to node-local services
- Per-node capability control
- Real-time WebSocket dashboard
- Bundled relay in the control plane (ZeroTier requires separate root servers)
- Single binary with embedded UI
- Role-based team access (admin/member with invite links)
- Audit logging (per-user and per-network)
- Device flow enrollment (RFC 8628)
- Bundle enrollment for air-gapped environments
- Short-lived certificates (24h auto-renew vs persistent keys)

**How they make money:** Free for personal use (25 devices). Commercial: $5/node/month (Essential), $10/node/month (Commercial). Enterprise: custom. They also sell ZeroTier Central (hosted controller) and support. Key insight: their per-node pricing matches ours. They monetize advanced features (SSO, flow rules, multipath) and commercial use.

**Key takeaway:** ZeroTier is the competitor that pushes hardest on raw networking capability and deployment flexibility. But their identity story is weak (flow rules not integrated with OIDC per their docs), their DNS is incomplete, and their UX is rougher. The opening: take the network-fabric depth (multi-network, routing, relay) but wrap it in a much better identity, naming, and operator experience. ZeroTier proves the market wants self-hosted + flexible networking; we can deliver it with better UX.

---

## Monetization Lessons

### What competitors charge for

| Revenue lever | DN | Tailscale | ZeroTier | Lesson for hopssh |
|---|---|---|---|---|
| **Per-host/node scaling** | $1/host | - | $5/node | Our $5/node matches ZeroTier. DN's $1 is low but they don't host infrastructure. |
| **Per-user scaling** | - | $6-18/user | - | Per-user pricing works when features are user-centric (SSO, posture, audit). Consider hybrid. |
| **Identity features** | SSO in free | SSO in paid ($6+) | SSO in paid | SSO is a paid-tier feature for most. We should gate it. |
| **Compliance features** | Audit in free | Session recording, log streaming in paid | Audit in paid | Session recording and log streaming are enterprise revenue drivers. |
| **Infrastructure** | Customer-hosted | Tailscale-hosted DERP | ZeroTier-hosted roots | We bundle infrastructure — this is a feature worth charging for. |
| **API & automation** | API in free, scoped keys in pro | Terraform/webhooks in paid | Terraform/webhooks in paid | API access can be free; scoped keys, webhooks, and IaC are paid features. |
| **Network capacity** | Routes: 2 free, 100 pro | Subnet routers in paid | Routes in paid | Route/subnet limits are a natural free→paid gate. |
| **Support** | Priority in Pro, dedicated in Enterprise | Support tiers | Support tiers | Support is expected at enterprise tier. |

### Pricing strategy recommendations

1. **Our $5/node vs DN's $1/host.** The 5x gap is justified if we communicate it clearly: hopssh bundles lighthouse, relay, DNS, web terminal, health dashboard, and proxy — DN charges $1 but requires customer-hosted infrastructure. Frame it as "all-inclusive" vs "assembly required."

2. **Consider per-user pricing for team features.** Tailscale proves per-user works when identity is the value. A hybrid model (base per-node for infrastructure + per-user for team/identity features) could work.

3. **Free tier size.** DN offers 100 free hosts. ZeroTier offers 25. We offer 10. Consider 25 to match ZeroTier — large enough for serious evaluation, small enough to convert.

4. **Enterprise feature gates.** SSO, session recording, log streaming, and RBAC are universally enterprise-tier features across all competitors. Gate these.

5. **Self-hosted is the moat.** All features free for self-hosted. This is our strongest differentiator against DN (SaaS only) and Tailscale (SaaS + Headscale). Never compromise this.

---

## Strategic Priorities

---

## User Pain Points by Competitor (Research: 2026-04-15)

Real complaints sourced from Reddit (r/selfhosted, r/homelab, r/Tailscale, r/zerotier), GitHub issues, Hacker News, LowEndTalk, blog posts, and app store reviews.

### ZeroTier — Top Pain Points

1. **Connections fall back to relay and stay there** — the #1 complaint by volume. No UI indicator for P2P vs relayed. `zerotier-cli peers` output is cryptic. Restarting sometimes fixes it, sometimes doesn't.
2. **Self-hosting the controller is painful** — no built-in web UI, third-party UIs are abandoned, Moon setup is undocumented, backup/restore is poorly documented.
3. **Mobile clients are unreliable** — iOS/Android disconnect in background, battery drain, apps lag behind desktop.
4. **DNS is manual and fragile** — no built-in DNS, must run separate Pi-hole/CoreDNS, manually configure /etc/hosts.
5. **Web UI is limited** — no bulk operations, no real-time status, no audit log, must authorize nodes one-by-one.
6. **Performance ceiling** — 50-100 Mbps cap, no hardware crypto acceleration, degrades at 50+ nodes.
7. **Pricing backlash** — free tier reduced from 100 to 25 nodes, seen as bait-and-switch.
8. **Security/trust concerns** — traffic through ZeroTier roots by default, custom protocol harder to audit.

### Tailscale — Top Pain Points

1. **Pricing** — per-user model ($6-18/user/month) seen as expensive. Free tier reduced from 100 to 3 users. #1 reason cited in "alternatives" threads.
2. **SaaS dependency** — closed-source coordination server, Tailscale knows your network graph, if Tailscale goes down your network breaks.
3. **Headscale is painful** — perpetually behind, breaking changes, no web UI, DERP self-hosting undocumented, Apple client compatibility breaks.
4. **No web terminal** — must install client on every device. No browser-based access from untrusted machines.
5. **MagicDNS reliability** — hijacks /etc/resolv.conf, breaks Pi-hole/AdGuard, stale records, Docker containers can't resolve.
6. **Subnet router flakiness** — connections drop, failover is slow, MTU issues, route conflicts.
7. **Key expiry headaches** — 180-day default, headless servers go offline, no good notification before expiry.
8. **ACL complexity** — HuJSON is confusing, no visual editor, error messages cryptic, can't test without pushing to production.
9. **Vendor lock-in anxiety** — pricing changes have happened, feature removal from free tier has happened.

### Defined Networking / Nebula — Top Pain Points

1. **Certificate management nightmare** — manual CA creation, cert distribution, rotation. The #1 reason people switch to Tailscale.
2. **Per-node configuration** — no central control plane pushing config. Adding a lighthouse means updating every node.
3. **SaaS-only (DN)** — no self-hosted option for managed Nebula. Raw Nebula is the alternative but with all the manual pain.
4. **Primitive DNS** — only resolves node hostnames, can't override OS resolver, no split-horizon, no custom domains.
5. **Performance gap vs WireGuard** — roughly half the throughput of kernel WireGuard.
6. **Mobile apps unstable** — "clunky and janky and crashy" (App Store reviews), no always-on reconnect.
7. **Customer-hosted infrastructure required** — even with Managed Nebula, you host your own lighthouses and relays.
8. **Small ecosystem** — no Terraform provider, no webhooks, limited community, few integrations.

### Cross-Competitor Universal Pains (user-reported)

These appear across ALL competitors, sourced from Reddit/HN/GitHub/app-store reviews:

| Pain | ZT | TS | DN | NetBird | hopssh Status |
|------|----|----|-----|---------|---------------|
| No connection type visibility (P2P/relay) | #1 | Notable | Notable | Notable | **Solved v0.9.10–v0.9.14** — per-node P2P/Mixed/Relayed badge, per-peer drill-down table, cytoscape topology diagram, persistent activity log with search/filter |
| Poor debugging / incident post-hoc | Notable | Notable | Notable | Notable | **Solved v0.9.14** — persistent `network_events` table + Activity tab with time-range, type filter, search, pagination |
| Self-hosting is hard or impossible | #2 | #2,#3 | #3 | ✅ they solved | **Solved** |
| DNS is broken/missing | #4 | #5 | #4 | ✅ they solved (Jan 2026) | **Solved** |
| No browser-based terminal | #5 | #4 | Notable | Notable | **Solved** (unique to hopssh) |
| Poor debugging/diagnostics | Notable | Notable | Notable | Notable | **Gap — adding to Phase 2A** |
| Mobile apps are unreliable | #3 | #9 | #6 | ⚠️ unverified | Not yet (Phase 3B) |
| Sleep/wake reconnection flaky | Notable (1-2 min) | Multi-year history of reports ([#1134](https://github.com/tailscale/tailscale/issues/1134), [#10688](https://github.com/tailscale/tailscale/issues/10688) still open) | Unverified | Unverified | Measured — <5 s dashboard flip post-wake on macOS/Linux/Windows via wake-triggered heartbeat (v0.9.11). See `spike/sleep-wake-evidence/RESULTS.md` + `spike/freshness-evidence/`. Bare-metal Linux/Windows suspend still unmeasured. |
| Pricing/lock-in anxiety | #7 | #1,#9 | — | — | **Solved** (self-hosted free forever) |

### Universal Unsolved Gaps (what NO competitor ships — research 2026-04-17)

These are the greenfield opportunities — features users need but no mesh VPN
currently delivers in production. These are the dimensions where hopssh can
claim a *first*, not a catch-up.

| Gap | Evidence | hopssh feasibility |
|-----|----------|--------------------|
| **Sleep/wake bulletproofness across all OS** | Tailscale: multi-year history of reports ([#1134](https://github.com/tailscale/tailscale/issues/1134) closed, [#10688](https://github.com/tailscale/tailscale/issues/10688) open, [#2173](https://github.com/tailscale/tailscale/issues/2173) closed, [#17736](https://github.com/tailscale/tailscale/issues/17736) closed) on macOS/Linux/Windows; ZeroTier "1-2 minutes to restore" (often worse — some users report 10-20 min or needing restart); NetBird unverified. No competitor claims this as solved. | **High** — measure hopssh first (30 min test); fix is 1-2 weeks if broken. `internal/quictransport/session.go` reconnect pattern is a foundation. |
| **DPI evasion / port-443 fallback for mesh VPN** | [NetBird #4879](https://github.com/netbirdio/netbird/issues/4879) documents users building wstunnel+nftables workarounds. [Mullvad shipped QUIC obfuscation](https://mullvad.net/en/blog/introducing-quic-obfuscation-for-wireguard) for WireGuard but that's for VPN-provider traffic, not mesh. | **High** — we have the building blocks in `internal/quictransport/` (unused by mesh today). Unique product angle for "works on hostile networks." |
| **Connection-type visibility + diagnostic topology view** | #1 ZeroTier complaint; noted for TS, DN, NetBird. | **Shipped (v0.9.10–v0.9.14)** — per-node P2P/Mixed/Relayed badges, per-peer drill-down, cytoscape topology diagram with pan/zoom preservation, persistent activity log. Still-missing: real-time per-peer RTT and route history (gated on roadmap #4 diagnostics). |
| **Adaptive MTU (DPLPMTUD / RFC 8899)** | Tailscale experimental, ZeroTier open request since 2016, WireGuard refuses. Nobody in production. | **Medium** — design done (`performance.md` §Phase 4), 2-3 weeks to build. All platforms. |

These four are the strategic frontier. Building any of them produces a
defensible "first" claim. Together they form a coherent "reliability + works
anywhere + you can see what's happening + it auto-optimizes" product story.

---

## Strategic Priorities (revised 2026-04-17)

The prior Tier 1-4 framework (beat DN → ops infra → pressure ZT → challenge
TS) is obsolete. Two shifts:

1. **NetBird, not Defined Networking, is the nearest structural competitor.**
   They're moving fast on self-hosted + SSO + DNS + reverse proxy — the same
   terrain we occupy. DN remains relevant but less central.
2. **Tailscale is vacating the "simple mesh for small teams" lane** in favor
   of enterprise AI + workload identity (see their March 2026 shipments).
   We should not chase them upward; we should own the space they're leaving.

### Tier 1: Own the universal unsolved gaps (first-in-class wins)

Where nobody is playing. Highest strategic leverage.

- **Sleep/wake resilience pass** (all OS) — measure first, fix if broken
- **DPLPMTUD (actually build it)** — design done, 2-3 weeks, genuine first
- **DPI evasion / port-443 MASQUE fallback** — reuses existing `quictransport/` code
- **Connection-type visibility + topology dashboard** — product UX win on #1 universal complaint

### Tier 2: Match NetBird on identity/policy (necessary parity)

Not losing to them on self-hosted SSO/firewall/API deals. Matches their 2026
feature set.

- SSO/OIDC/SAML (they have it in free tier)
- SCIM provisioning
- Scoped API keys
- Granular firewall: groups + tags + rules editor
- Subnet routing / exit nodes
- Expand free tier to 25 nodes

### Tier 3: Performance leadership across all OS

- macOS: preserve lead (`sendmsg_x`/`recvmsg_x` batch, patches 04-10 shipped)
- Linux: GSO/GRO + checksum + crypto vector (parity + catch-up to Tailscale)
  — see [linux-throughput-plan.md](linux-throughput-plan.md)
- Windows: RIO (Registered I/O) — real win for userspace VPNs, narrower than
  first claimed (kernel WireGuardNT went WSK instead)
- Cross-platform: vectorized crypto pipeline amplifies the platform I/O work

### Tier 4: Long tail

- Mobile clients (iOS, Android) built on the resilience foundation from Tier 1
- Desktop tray apps (macOS, Windows)
- Webhooks, log streaming, Terraform provider (enterprise ops)
- Session recording, policy grants-style framework (TS-parity long game)

### Explicitly NOT chasing

- **Smart pacing / BBR for WiFi airtime** — research confirms MAC-layer
  problem, not solvable from userspace ([arxiv.org/html/2512.18259v1](https://arxiv.org/html/2512.18259v1)).
- **Multipath bonding as a novel differentiator** — [Speedify](https://speedify.com/)
  has shipped real WiFi+cellular bonding since 2014; ZeroTier has protocol-level
  multipath. If we pursue it, frame as "catching up," not first-in-class. 3-6
  months minimum effort, not 6-8 weeks.
- **Tailscale's enterprise feature stack** (Aperture AI gateway, workload
  identity federation, session recording) — they have a 4+ year head start;
  chasing them means arriving late with the same features.

### Performance leadership (verified shipped)

- macOS `sendmsg_x`/`recvmsg_x` batch syscalls (patches 04-10) — **only VPN
  using private XNU batch syscalls**; 17% → 35-53% tunnel efficiency
- macOS control-lane priority queue (patches 09-10)
- TUN buffer caching (patch 03) + AES-GCM hardware acceleration on Apple Silicon

### The product thesis (revised)

> Self-hosted mesh VPN built on Nebula, with stronger crypto isolation than any
> competitor, verified performance leadership on macOS, and the first mesh VPN
> to solve the universal resilience gaps (sleep/wake, DPI, diagnostics, adaptive
> MTU) that every other vendor has left on the table.

This works because:
- **NetBird** is beating everyone on self-hosted identity but built on single-tenant
  WireGuard — no per-network crypto isolation, no browser terminal, no bundled relay
- **Tailscale** is leaving the "simple self-hosted mesh" lane for enterprise AI
- **ZeroTier** controller moved to source-available in July 2025; users are
  actively migrating away
- **Defined Networking** is SaaS-only and doesn't host your infrastructure
- **hopssh** can own the universal resilience gaps + Nebula isolation + single-binary
  elegance + browser terminal, while closing identity parity with NetBird on a
  predictable schedule
