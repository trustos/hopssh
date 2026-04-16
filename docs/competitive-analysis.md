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
| Connectivity/topology map | No | No | No | No |
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

### Cross-Competitor Universal Pains

These appear across ALL three competitors:

| Pain | ZT | TS | DN | hopssh Status |
|------|----|----|-----|--------------|
| No connection type visibility (P2P/relay) | #1 | Notable | Notable | **Gap — adding to Phase 2A** |
| Self-hosting is hard or impossible | #2 | #2,#3 | #3 | **Solved** |
| DNS is broken/missing | #4 | #5 | #4 | **Solved** |
| No browser-based terminal | #5 | #4 | Notable | **Solved** |
| Poor debugging/diagnostics | Notable | Notable | Notable | **Gap — adding to Phase 2A** |
| Mobile apps are unreliable | #3 | #9 | #6 | Not yet (Phase 3B) |
| Pricing/lock-in anxiety | #7 | #1,#9 | — | **Solved** (self-hosted free forever) |

---

### Tier 1: Beat Defined Networking (nearest competitor, easiest to surpass)

We already win on self-hosting, terminal, DNS, bundled infrastructure, and setup simplicity. To decisively beat DN:
- Add SSO/OIDC (they have it, we don't)
- Add scoped API keys (they have them, we have schema but no implementation)
- Add granular firewall with roles + tags (they have it, we only have capabilities)
- Expand free tier to 25 nodes (closer to competitive)

### Tier 2: Build operator infrastructure (makes us enterprise-credible)

Neither DN nor ZeroTier is strong here. Tailscale is the benchmark. Close the gap:
- Webhooks for events (Tailscale and ZeroTier have them)
- Log streaming / SIEM export (Tailscale has it, nobody else does well)
- Terraform provider (Tailscale and ZeroTier have them)
- GitOps config export/import

### Tier 3: Pressure ZeroTier (steal the "self-hosted networking" market)

ZeroTier owns "self-hosted overlay network." Take it with better UX:
- Regional relay nodes via dashboard (they have private roots)
- Peer connectivity map (neither has a good one)
- Subnet routing / exit nodes (they have managed routes)
- Better multi-network ergonomics

### Tier 4: Challenge Tailscale (identity-aware access platform)

This is the long game. Tailscale's moat is identity + policy + compliance:
- Grants-like unified policy model
- Device posture / approval
- App connectors (domain-based routing)
- SSH session recording
- SCIM provisioning
- Desktop and mobile apps

### Performance Leadership

- macOS `sendmsg_x`/`recvmsg_x` batch syscalls — only VPN using private XNU batch-send; 17% → 35-53% tunnel efficiency
- macOS control-lane priority queue — handshakes/lighthouse ahead of bulk data
- TUN buffer caching + AES-GCM hardware acceleration on Apple Silicon

### The product thesis

> Self-hosted private access built on Nebula, with better trust than Defined, better policy ergonomics than ZeroTier, and enough identity/app access depth to challenge Tailscale where sovereignty matters.

This works because:
- **Defined** is cloud-managed with no self-hosted option and no bundled infrastructure
- **Tailscale** has the best identity/access UX but is fundamentally SaaS-coordinated
- **ZeroTier** has strong self-hosted networking but rough identity/policy/naming
- **hopssh** can combine self-hosted sovereignty + bundled infrastructure + identity-aware access in one product
