# hopssh — Research & Market Analysis

## Problem Statement

Every team with >1 person sharing servers faces the same pain:

1. **SSH Key Sprawl** — Average company has 10x more SSH keys than employees. Keys never expire, never get rotated, never get revoked when someone leaves.
2. **Onboarding Friction** — New developer joins → 2-3 days before they can access anything. Each environment has its own access setup.
3. **Offboarding Risk** — Someone leaves → their keys linger for months. Contractors get permanent keys that are never removed.
4. **Bastion/VPN Bottleneck** — Hub-and-spoke forces all traffic through one point. Bastion hosts need patching, monitoring. VPN splits across environments.
5. **"How Do I Reach This Server?"** — Private subnet, no public IP. Which bastion? Which VPN? Documentation always outdated.
6. **Compliance Gaps** — "Who accessed production at 3am?" → nobody knows. SOC 2 auditors flag SSH key management every quarter.

## Competitive Landscape

| Product | Model | Price | What it does | Gap |
|---|---|---|---|---|
| **Tailscale** | Mesh VPN | $6/user/mo | WireGuard mesh, MagicDNS, ACLs | Network only — still need SSH client, key management |
| **Teleport** | Access platform | $15-40/user/mo | SSH, K8s, DB access, audit, RBAC | Heavy, complex, expensive for small teams |
| **Cloudflare Tunnel** | Reverse tunnel | $7/user/mo (Zero Trust) | HTTP tunnel, browser SSH | HTTP-focused, limited TCP, Cloudflare lock-in |
| **StrongDM** | PAM | ~$70/user/mo | Privileged access, dynamic creds | Enterprise-only pricing, complex setup |
| **Boundary** | Access proxy | Free OSS / HCP paid | Session-based access, identity-aware | No web terminal, requires HashiCorp ecosystem |
| **Guacamole** | Gateway | Free OSS | Browser SSH/RDP/VNC | Manual setup, no mesh, no auto-enrollment |
| **Warpgate** | Bastion | Free OSS | Smart SSH/HTTP/MySQL bastion | Still requires inbound port, no mesh |

## Where hopssh fits

```
                    Simple ←————————————————→ Complex
                    
  SSH keys     Tailscale+SSH    hopssh    Teleport    StrongDM
  (free,         ($6/user,     ($5/node,  ($15-40/    ($70/user,
   painful)      still SSH)    browser,    user,       enterprise)
                               no keys)   heavy)
```

**hopssh occupies the gap between "Tailscale + SSH client" and "Teleport":**
- Simpler than Teleport (no complex setup, no K8s concepts)
- More complete than Tailscale (browser terminal, no SSH keys needed)
- Cheaper than both for small teams
- Self-hostable (unlike StrongDM, Cloudflare)

## Target Users

| Segment | Size | Why they'd pay | Entry point |
|---|---|---|---|
| Solo devs with VPS | Very large | Free tier, habit building | Blog posts, HN, Twitter |
| Small dev teams (2-15) | Large | SSH key sharing nightmare, onboarding | Word of mouth from solo devs |
| Agencies managing client infra | Medium | Client rotation, contractor access | Direct outreach |
| Compliance-bound teams | Medium | Audit trail, access controls | SOC 2 pain |
| DevOps with hybrid/multi-cloud | Medium | Unified access across environments | Terraform provider |

## Adoption Strategy (bottom-up)

```
Blog post / HN Show / tweet
  "Access any server from your browser in 60 seconds"
        ↓
Solo dev tries it on VPS                         ← FREE (5 nodes)
        ↓
Uses it daily, tells coworkers
        ↓
Team of 5 uses it for staging                    ← FREE (under 5 nodes)
        ↓
Team grows, adds production (12 nodes)           ← PAID ($60/mo)
        ↓
Company scales, needs audit + SSO                ← ENTERPRISE
```

## Technical Differentiators

1. **Zero-config mesh** — PKI auto-generated per network, agent auto-enrolls, no manual Nebula/WireGuard setup
2. **Outbound-only agent** — Works behind NAT, firewalls, private subnets. No inbound ports. No security group changes.
3. **No stored credentials** — Control plane never holds SSH keys, cloud creds, or server passwords. Agent-mediated access only.
4. **Per-network isolation** — Each network gets its own CA. Cryptographic isolation, not just ACLs.
5. **Browser-native** — Web terminal with PTY, resize, color. No local tools needed.
6. **IaC integration** — Terraform/Pulumi provider for auto-bootstrap on new infrastructure.

## Revenue Model

Conservative estimates for first 18 months:

| Month | Free users | Paid teams | Nodes (paid) | MRR |
|---|---|---|---|---|
| 1-3 | 50-200 | 0 | 0 | $0 |
| 4-6 | 500-1,000 | 5-10 | 50-100 | $250-500 |
| 7-12 | 2,000-5,000 | 20-50 | 200-500 | $1,000-2,500 |
| 13-18 | 5,000-10,000 | 50-100 | 500-1,000 | $2,500-5,000 |

Break-even (infrastructure + time costs) at ~$2,000/mo MRR.

## Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Tailscale adds web terminal | Medium | High | Move fast, build community, self-hosted option |
| Security incident with agent | Low | Critical | Agent runs minimal surface, no stored creds, bug bounty |
| Low conversion free→paid | High | Medium | Generous free tier drives adoption, team features drive conversion |
| Support burden | Medium | Medium | Good docs, community forum, self-service |

## Origin

Core technology extracted from [pulumi-ui](https://github.com/trustos/pulumi-ui) mesh/agent system.
Production-tested in OCI infrastructure deployments with Nebula overlay, auto-PKI, and web terminal.
