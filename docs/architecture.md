# hopssh — Architecture

Encrypted mesh networking with P2P, relay fallback, built-in DNS, and a web terminal.

> **Note (2026-05-09):** this doc captures the foundational architecture (Phase 1, mesh core). For architecture details added in v0.10+ (multi-network per agent, three-watchdog reliability, system-mode mirror handoff, macOS desktop client, in-app terminal via dashboard webview), see the wiki concept pages: [`docs/wiki/concepts/`](wiki/concepts/) — `client-strategy`, `desktop-client`, `macos-system-mode`, `watchdog`, `cert-renewal`, `sleep-wake`. The phase ledger at [`docs/wiki/phases/phase-ledger.md`](wiki/phases/phase-ledger.md) is the version-by-version record.

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
- **P2P primary, relay fallback.** Most connections succeed as direct P2P. Two paths to direct P2P: (1) classic UDP hole punching for cone-NAT pairs; (2) **NAT-PMP port mapping (v0.10.3+)** for asymmetric setups where one peer is behind a UPnP/NAT-PMP-capable home router and the other is behind random-port symmetric CGNAT (cellular). The home peer asks its router to forward a public UDP port; the resulting endpoint is injected into the lighthouse's `advertise_addrs` (vendor patch 11); the cellular peer reaches it directly. Only bidirectional random-port symmetric NAT (both peers cellular) currently falls back to relay — birthday-paradox port prediction is planned (roadmap N4).
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

### Node (`cmd/agent`)

Installed on any device (server, laptop, phone, NAS). Single binary with embedded Nebula.
All nodes are equal — capabilities (terminal, health, forward) are per-node toggles.

| Component | Purpose |
|-----------|---------|
| **Nebula** (embedded, dual-mode) | Kernel TUN (real OS interface) when root, userspace (gvisor) when non-root |
| **HTTP server** (on mesh) | /health, /exec, /shell, /upload — controlled by capabilities |
| **Cert renewal** | Auto-renews 24h certificates with jitter (±10%) |
| **Heartbeat** | Reports online status + per-peer connectivity counts to control plane every 60 s; also fires out-of-cycle on detected wake / network change so the dashboard sees state updates within seconds |
| **Split-DNS** | Configures OS resolver for mesh domain (kernel TUN mode) |
| **CLI** | help, status, info, enroll, serve, install, update |

**TUN modes:**
- **Kernel TUN** (default when root) — creates a real OS network interface (`utun` on macOS, `tun` on Linux). Mesh IPs are routable at the OS level — `ping`, `ssh`, `curl` work directly. Requires root.
- **Userspace** (default when non-root) — in-process virtual networking via gvisor netstack. No OS interface, connectivity only through the agent process. No root required.
- **Graceful fallback** — if kernel TUN fails (containers, missing permissions), automatically falls back to userspace, then to OS stack.

Override with `--tun-mode kernel` or `--tun-mode userspace`.

Capabilities are toggled per-node from the dashboard:
- **terminal** — web terminal (PTY) access from browser
- **health** — health check endpoint
- **forward** — TCP port forwarding through mesh

---

## Multi-network per agent (v0.10.0+)

A single agent process can join 2+ networks simultaneously. Each network is an independent `meshInstance` with its own Nebula CA, lighthouse, DNS domain, listen port, and renewal/heartbeat goroutines.

### On-disk layout

```
<configDir>/
  enrollments.json           Index — name → endpoint → network UUID → listen port
  enrollments.json.bak       Sibling backup (rewritten after every successful save)
  home/                      Per-enrollment subdir (one per network)
    node.crt
    node.key
    ca.crt
    nebula.yaml              listenPort = 4242, dev = hop-home
    agent-token.enc
    peers.json
    tun-mode                 "kernel" | "userspace" — runtime-mutable
  work/
    nebula.yaml              listenPort = 4243, dev = hop-work
    ...
```

### Critical invariants

- **Per-enrollment listen ports.** Each enrollment gets a deterministic port (4242, 4243, 4244, …) allocated by `enrollmentRegistry.NextAvailableListenPort`. Random kernel-assigned ports shift across restarts — defeating NAT-PMP, leaving the lighthouse with stale `udpAddrs`, and hanging direct P2P. The port is persisted in the enrollment registry.
- **Per-enrollment kernel TUN device names.** On Linux, two instances cannot both create `nebula1` (EEXIST). Each enrollment writes `dev: hop-<enrollment>` into its `nebula.yaml`, truncated to IFNAMSIZ=15. macOS ignores this knob (kernel auto-allocates `utunN`) but Linux + Windows require unique names.
- **PKI paths re-resolved on every boot.** `ensureP2PConfig` rewrites `pki.ca/cert/key` in `nebula.yaml` to match `inst.dir()` on every start — idempotent for correct configs, self-healing for migrated/drifted ones.
- **Same-network duplicate rejection.** `existingEnrollmentForNetwork(reg, endpoint, caFingerprint)` blocks a second enroll into the same network, even if the user picks a different local name.
- **Live runtime add/remove (v0.10.34).** `POST /local/connect` and `POST /local/disconnect` add/remove instances without restarting the agent. Wires through `connectFn`/`disconnectFn` callbacks from `runServe` into the local API. Failure during runtime connect propagates an error instead of `log.Fatal`-ing.

### Why N-instance-per-network is the canonical model

Defined Networking's [multi-network guide](https://www.defined.net/blog/multiple-networks/) and Nebula's own design require this shape — single-instance multi-overlay has been requested upstream since 2020 ([slackhq/nebula#235](https://github.com/slackhq/nebula/issues/235), [#251](https://github.com/slackhq/nebula/issues/251), [#306](https://github.com/slackhq/nebula/issues/306)) with no implementation. The blockers are in Nebula's trust model (per-overlay lighthouse subnet check, per-instance firewall scope), not in our code. A `pki.ca` PEM bundle of multiple CAs is for CA rotation — it does NOT enable joining multiple overlays from one instance.

---

## Three-watchdog reliability architecture

Three independent watchdogs cover three distinct silent-failure modes. All three share the same primitive: **stamp + age threshold + cooldown + restartFn**, where `restartFn` closes over the v0.10.34 lifecycle infrastructure (close old svc, reconnect a fresh `meshInstance`).

| Watchdog | Detects | Stamp | Threshold | Shipped |
|---|---|---|---|---|
| **Renewal silent-death** | Renewal goroutine wedged or panicked | `lastRenewalActivityAt` (top of every renewal tick + before/after each POST) | `expectedCertValidity / 4` (~6h on 24h certs) | Phase P (v0.10.79) |
| **Stuck data plane** | Mesh has peers but every probe fails | `peers > 0 && probed > 0 && succeeded == 0` for `watchdogStuckThreshold` (3) cycles | ~4.5 min of confirmed stuck-state | v0.10.36 |
| **watcher wedge** | `watchNetworkChanges` deadlocked inside vendor Nebula call | `lastWatcherActivityAt` (top of every tick body) | 3 minutes | Phase DD (v0.10.96) |

### Common shape (internal/client/{renew_watchdog,keepalive,watcher_watchdog}.go)

```go
// All three follow the same structure:
//   1. Long-running goroutine stamps activity at the TOP of every iteration.
//   2. Watchdog goroutine ticks every 30s, compares stamp age to threshold.
//   3. On trip: write goroutine pprof dump to <configDir>/<name>/<class>-stuck-<ts>.txt,
//      log CRITICAL, invoke inst.restartFn (subject to 5-min cooldown).
//   4. inst.restartFn closes over connectFn(name) — the v0.10.34 lifecycle that
//      tears down the dead instance and starts a fresh meshInstance from disk.
```

### Why three separate watchdogs

The three classes can fail independently. Phase P's renewal watchdog protects only `lastRenewalActivityAt`. v0.10.36's keepalive watchdog requires `peers > 0` (false for hours after the lighthouse-filter post-Phase V left only one peer that drifted offline). Phase DD's watcher wedge at `internal/client/nebula.go:272-273` (vendor `RebindUDPServer` / `CloseAllTunnels` deadlock) leaves heartbeat firing fine — UI shows green, mesh is dead. Each pair of stamps is orthogonal; one watchdog cannot substitute for another.

### Hard timeouts on vendor Nebula calls (Phase DD F2)

`watchNetworkChanges`'s rebind block wraps `RebindUDPServer` and `CloseAllTunnels` in `runWithTimeout(name, label, 5*time.Second, fn)` so the watcher self-recovers without needing F1's auto-restart in the common case. The leaked goroutine on timeout is the accepted cost — alternative is the entire watcher wedging for hours. `defer recover()` only catches panics, not deadlocks; only timeouts catch this.

### UI honesty axis (Phase DD F3)

`enrollmentStatus.Connected` derives from `certValid && watcherAlive && (activeFlow || recentHeartbeat)`. Without `watcherAlive`, UI shows green for hours while the data plane is dead because heartbeat is on a separate goroutine and stays fresh. The `recentHeartbeat` axis alone does NOT protect against this; only watcher-stamp freshness does.

See [`docs/wiki/concepts/watchdog.md`](wiki/concepts/watchdog.md) for the full design + per-watchdog forensic dump format.

---

## System-mode mirror handoff (macOS)

The macOS desktop client's mental model: bundled child agent (`hopssh.app/Contents/Resources/binaries/hop-agent`) for first-run, opt-in upgrade to a system-mode `LaunchDaemon` (`/Library/LaunchDaemons/com.hopssh.agent.plist`) that owns the mesh state-of-record and runs as root with kernel-utun. Both modes expose a loopback HTTP local-API; the .app must attach to whichever is live.

### Mirror files

When the LaunchDaemon (or any system-mode agent) starts, it writes two files into the user's home:

```
~/Library/Application Support/hopssh/
  system-local-api-port    Mode 0644 — readable by the user (PID:port:expiry)
  system-local-api-token   Mode 0600 — bearer token, owner-readable only
```

The `.app`'s Tauri shell reads these on launch and on file-change events (notify crate kqueue). Phase X's TCP-probe (`endpoint_alive`) validates the cached endpoint before treating it as healthy — kernel sockets can be silently dead even when "still set" in a state pool. Phase W's 10×100ms retry budget (`try_attach_to_system_agent`) handles the launch race where the daemon is mid-respawn.

### Boot-before-login chown self-heal (Phase Z)

Mirror files are user-owned (so the .app can read them) but the LaunchDaemon runs as root. `chownMirrorFiles` resolves the chown target via two-layer fallback: (1) `resolveConsoleUser` if `/dev/console` is owned by a real user; (2) stat the mirror dir's owner (created by the user during `hop-agent install --migrate-from`, so dir owner = correct chown target — independent of `/dev/console` state). This catches the boot-before-login window where `/dev/console` is root-owned. `runMirrorChownSelfHeal` (30s tick) re-runs the chown if files revert to root-owned — once chown succeeds the loop becomes a no-op.

### Lifecycle

| Event | What happens |
|---|---|
| .app launch, mirror files exist | Tauri's `try_attach_to_system_agent` reads files, TCP-probes the port, attaches |
| .app launch, mirror files missing | Spawn bundled child agent (gvisor userspace, runs as user) |
| LaunchDaemon restart (kickstart, dev-deploy, crash) | Mirror files atomic-rewrite with new port + token; notify watcher fires; .app re-attaches |
| Notify event missed | Phase X periodic re-probe (5s for first 60s, 30s after) catches it |
| User clicks Retry on Disconnected screen | `retry_attach_system_agent` Tauri command forces explicit re-probe |
| convert_to_system_service | Spawn `osascript … with administrator privileges` script that installs LaunchDaemon, kicks bundled child to die, .app re-attaches via `agent-ready` event |
| revert_to_bundled | Daemon stop + uninstall, mirror files removed, .app re-spawns bundled child |
| uninstall_hopssh_full | Disable autostart (Phase AA F2) BEFORE running privileged uninstall script — plugin's `disable()` removes plist AND unloads from launchd; just deleting the file leaves a brief window where launchd has it loaded |

See [`docs/wiki/concepts/macos-system-mode.md`](wiki/concepts/macos-system-mode.md) for the full handoff diagram + mirror-token rotation.

---

## Desktop client architecture (macOS, v0.10.85+)

Tauri 2 + Svelte 5 menubar app. Sidecar `hop-agent` binary (bundled in `hopssh.app/Contents/Resources/binaries/`). Cross-platform Svelte UI; per-platform Rust shell wraps OS-specific machinery.

### Process model

```
hopssh.app (PID 1)
├── Tauri 2 Rust shell (lib.rs)
│   ├── Tray icon (NSStatusItem, template-mode)
│   ├── Menubar window (Svelte UI hosted by WKWebView)
│   ├── Optional Terminal webview windows (one per peer SSH session)
│   └── 22 Tauri commands exposed to JS (status, enroll, leave, set_*, …)
└── Bundled mode only:
    └── hop-agent child (gvisor userspace, runs as user)
        └── Loopback local-API on 127.0.0.1:RAND port

System-mode (post-convert):
hopssh.app (PID N)         /Library/LaunchDaemons/com.hopssh.agent.plist
├── Rust shell                ↓
└── Reads mirror files     hop-agent (root, kernel-utun)
    + attaches               └── Loopback local-API on 127.0.0.1:RAND port
```

### Critical features (Phase V → II.4)

| Feature | Phase | Mechanism |
|---|---|---|
| Member-role enrollment | V (v0.10.85) | Server's `/api/enroll`/`/api/device/authorize` relaxed from `CanAccessNetwork` to `CanEnrollNode`; `existingEnrollmentForNetwork` prevents orphan nodes |
| Stale endpoint recovery | X (v0.10.89) | TCP-probe `endpoint_alive` validates cached endpoint before treating as healthy |
| Autostart on login | Y (v0.10.90) | tauri-plugin-autostart writes `~/Library/LaunchAgents/com.hopssh.desktop.plist`; auto-enable on first convert-to-system if `start_at_login_explicit == false` |
| OS Settings → Login Items registers desktop autostart | Y | `hop-agent install` + LaunchDaemon's `RunAtLoad: true` are *agent* persistence; the .app needs its own LaunchAgent — these are decoupled by design |
| Self-heal mirror chown | Z (v0.10.91) | `chownMirrorFiles` two-layer fallback + `runMirrorChownSelfHeal` 30s loop |
| Three-watchdog ride-along | DD (v0.10.96) | UI flips to "agent connection issue — recovering automatically" within ~30s of watcher silence |
| In-app Activity tab | FF (v0.10.98) | Subscribes to agent's existing SSE event stream; ring buffer ~100 events |
| Diagnostics tools | GG (v0.10.99) | "View agent logs" → `osascript` Console.app filtered to hopssh process |
| Read-only DNS records | HH (v0.11.0) | Surfaces records via existing peer-info data flow; "Manage in dashboard" link for create/delete |
| In-app Terminal | II.3 (v0.11.3) | `open_terminal_webview` Tauri command opens new webview pointed at dashboard's `/terminal/{networkId}/{nodeId}` route, cookie-shared with main window |
| OS brand-mark icons | II.4 (v0.11.4) | Inline SVG (Apple/Tux/Windows-pane) on desktop and dashboard peer rows; `title=` tooltip on desktop + shadcn `<Tooltip>` on dashboard |

### Why webview Terminal (Phase II.3) over xterm.js shipped inside the .app

Reuse-the-existing-component path. The dashboard already ships a working xterm.js + WebSocket-proxy at `/terminal/{networkId}/{nodeId}`. Tauri webviews share cookie storage with the main window, so first-launch login persists. Three benefits over re-implementing in the .app: (1) one Svelte UI codebase across desktop + dashboard; (2) WebSocket auth uses the same session cookie path the dashboard already uses; (3) the dashboard's terminal route ships in lockstep with the .app — no version skew between two implementations.

See [`docs/wiki/concepts/desktop-client.md`](wiki/concepts/desktop-client.md) for the 22-command Tauri command catalog + post-ship-update inventory.

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
       exposed_ports, dns_name, capabilities, status, last_seen_at, created_at)
  └─ node_type: "node" (unified), "lighthouse" (control plane internal)
  └─ capabilities: JSON array ["terminal","health","forward"] — per-node toggles
  └─ exposed_ports: JSON array of {port, proto, name} for mesh firewall
  └─ dns_name: auto-sanitized from hostname (lowercase, strip .local)
  └─ enrollment_token: SHA-256 hashed, single-use, 10-min TTL
  └─ agent_token: AES-GCM encrypted, constant-time comparison
  └─ nebula_key: AES-GCM encrypted
  └─ status: pending → enrolled → online → offline

network_members (id, network_id, user_id, role, created_at)
  └─ role: "admin" (owner/full access) or "member" (view + join)
  └─ UNIQUE(network_id, user_id)

network_invites (id, network_id, created_by, code, role,
                 max_uses, use_count, expires_at, created_at)
  └─ shareable invite links with expiry + max uses + role selector
  └─ code: 32-byte hex, single-use atomic claim

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
  └─ buffered write path: 2 s flush, 100-entry batch, drop-on-overflow

nodes.agent_version TEXT (nullable, v0.9.15+)
  └─ self-reported hop-agent build, carried on each heartbeat via the
     existing COALESCE pattern; NULL preserves prior value.
     Dashboard compares with control plane's /version and highlights drift.

network_events (id, network_id, event_type, target_id, status, details, created_at)
  └─ persistent activity log — every WebSocket event is also appended here
  └─ event_types: node.enrolled, node.status, node.renamed, node.capabilities,
                  node.deleted, dns.changed, member.changed
  └─ `node.status` persisted only on transition (online ↔ offline), NOT every heartbeat
  └─ indexed by (network_id, created_at DESC) and (network_id, event_type, created_at DESC)
  └─ buffered write path: same 2 s flush / 100-entry batch pattern as audit_log
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

1. Each network runs a DNS server on the control plane (unique UDP port per network)
2. The DNS server resolves `<hostname>.<domain>` by looking up nodes in the database
3. Auto-generated records: every node with a hostname gets a record automatically
4. Custom records: users can add aliases (e.g., `jellyfin` pointing to a node's IP)
5. Agents in kernel TUN mode automatically configure OS split-DNS during enrollment

### Split DNS configuration (automatic in kernel TUN mode)

When the agent starts in kernel TUN mode, it configures the OS to route mesh domain
queries to the control plane's DNS server:

| Platform | Method | What happens |
|----------|--------|-------------|
| macOS | `/etc/resolver/<domain>` | File created with nameserver + port pointing to control plane |
| Linux (systemd-resolved) | `resolvectl` | DNS + domain set on the TUN interface |
| Linux (fallback) | `/etc/resolver/<domain>` | Resolver file created if systemd-resolved unavailable |

DNS configuration is automatically cleaned up when the agent stops.

Regular internet DNS is unaffected — only queries for the mesh domain go through the mesh DNS.

---

## Firewall Groups

Nebula certificates carry groups. hopssh uses a unified model:

| Group | Assigned to | Purpose |
|-------|------------|---------|
| `admin` | Control plane (lighthouse) | Can reach management API on all nodes |
| `node` | All enrolled nodes | Can reach other nodes on the mesh |

Access control beyond network-level is handled by **per-node capabilities** at the application layer, not Nebula firewall groups. This follows the Tailscale/ZeroTier model.

### Node firewall (generated during enrollment)

```yaml
firewall:
  inbound:
    # Control plane can reach node management API
    - port: 41820
      proto: tcp
      groups: [admin]
    # All mesh nodes can reach each other
    - port: any
      proto: tcp
      groups: [node]
    # ICMP for diagnostics
    - port: any
      proto: icmp
      host: any
  outbound:
    - port: any
      proto: any
      host: any
```

Per-node capabilities (terminal, health, forward) are checked at the control plane proxy layer, not the Nebula firewall. This allows toggling capabilities from the dashboard without re-issuing certificates.

---

## API Endpoints

### Public (no auth)
| Method | Path | Purpose |
|--------|------|---------|
| GET | `/healthz` | Health check for orchestrators (no rate limit) |
| GET | `/version` | Latest available version JSON |
| GET | `/install.sh` | Install script with endpoint pre-baked |
| GET | `/download/{binary}` | Redirect to GitHub Release binary |
| GET | `/api/auth/status` | Check if any users exist |
| POST | `/api/auth/register` | Create account |
| POST | `/api/auth/login` | Login → session cookie |
| POST | `/api/enroll` | Token-based node enrollment |
| POST | `/api/device/code` | Device flow: request code |
| POST | `/api/device/poll` | Device flow: agent polls |
| POST | `/api/renew` | Node cert renewal (bearer token) |
| POST | `/api/heartbeat` | Node heartbeat (bearer token) |
| GET | `/api/bundles/{token}` | Download enrollment bundle |
| GET | `/api/invites/{code}` | Invite details (for accept page) |

### Authenticated (session cookie)
| Method | Path | Purpose |
|--------|------|---------|
| POST | `/api/auth/logout` | Destroy session |
| GET | `/api/auth/me` | Current user info |
| **Networks** | | |
| POST | `/api/networks` | Create network |
| GET | `/api/networks` | List networks (owned + member) |
| GET | `/api/networks/{id}` | Network detail + nodes (with role) |
| DELETE | `/api/networks/{id}` | Delete network (admin only) |
| **Nodes** | | |
| POST | `/api/networks/{id}/nodes` | Generate enrollment token |
| GET | `/api/networks/{id}/nodes` | List nodes |
| PATCH | `/api/networks/{id}/nodes/{nodeId}` | Rename node |
| PUT | `/api/networks/{id}/nodes/{nodeId}/capabilities` | Update capabilities |
| DELETE | `/api/networks/{id}/nodes/{nodeId}` | Delete node |
| GET | `/api/networks/{id}/nodes/{nodeId}/health` | Health check (capability gated) |
| GET | `/api/networks/{id}/nodes/{nodeId}/shell` | WebSocket terminal (capability gated) |
| POST | `/api/networks/{id}/nodes/{nodeId}/exec` | Command exec (capability gated) |
| **Port Forwards** | | |
| POST | `/api/networks/{id}/nodes/{nodeId}/port-forwards` | Start forward (capability gated) |
| DELETE | `/api/networks/{id}/port-forwards/{fwdId}` | Stop forward |
| GET | `/api/networks/{id}/port-forwards` | List active forwards |
| **DNS** | | |
| GET | `/api/networks/{id}/dns` | List DNS records |
| POST | `/api/networks/{id}/dns` | Add custom DNS record (admin) |
| DELETE | `/api/networks/{id}/dns/{recordId}` | Remove DNS record (admin) |
| **Members** | | |
| GET | `/api/networks/{id}/members` | List members |
| DELETE | `/api/networks/{id}/members/{memberId}` | Remove member (admin) |
| **Invites** | | |
| POST | `/api/networks/{id}/invites` | Create invite (admin) |
| GET | `/api/networks/{id}/invites` | List invites (admin) |
| DELETE | `/api/networks/{id}/invites/{inviteId}` | Revoke invite (admin) |
| POST | `/api/invites/{code}/accept` | Accept invite |
| **Events** | | |
| GET | `/api/networks/{id}/events` | WebSocket real-time events |
| GET | `/api/networks/{id}/events/history` | Persistent activity log (`?since=&type=&limit=`) |
| GET | `/api/audit` | User-scoped audit log (`?since=&action=&limit=`) |
| GET | `/api/networks/{id}/audit` | Network-scoped audit log (`?since=&action=&limit=`) |
| **Other** | | |
| POST | `/api/networks/{id}/join` | Join network (issues cert) |
| POST | `/api/device/authorize` | Authorize device code |
| GET | `/api/device/verify/{code}` | Check device code |
| POST | `/api/networks/{id}/bundles` | Generate enrollment bundle |

---

## Enrollment

See [enrollment.md](enrollment.md) for detailed user flows and examples.

### Node enrollment
Four modes, all produce identical nodes with `node` cert group:

1. **Device flow** (default, interactive): `hop-agent enroll --endpoint <url>`
2. **Token stdin** (scriptable): `echo '<token>' | hop-agent enroll --token-stdin --endpoint <url>`
3. **Token arg** (quick): `hop-agent enroll --token <token> --endpoint <url>`
4. **Bundle** (air-gapped): `hop-agent enroll --bundle <path>`

All modes issue a Nebula certificate with group `node`, configure the mesh, and auto-install the service. Use `--force` to re-enroll an already-enrolled device.

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
     │  Node A  │ │  Node B  │
     │  Has:    │ │  Has:    │
     │  cert    │ │  cert    │
     │  (node)  │ │  (node)  │
     │  token   │ │  token   │
     └──────────┘ └──────────┘
         ↕ P2P (direct or relayed)
```

### What makes it safe
1. **E2E encryption** — relay cannot read traffic. Only endpoints with valid certs can communicate.
2. **Per-network CA** — compromising one network's CA has zero effect on others.
3. **Short-lived certs (24h)** — auto-renewed with jitter. Node deletion = cert not renewed = access revoked within 24h.
4. **Per-node capabilities** — terminal, health, forward checked at application layer. Toggleable without re-enrollment.
5. **No inbound ports** — nodes connect outbound to the lighthouse. Even in kernel TUN mode, the agent initiates all connections.
6. **Single binary** — no dependency chain, no supply chain attack surface beyond Go stdlib + Nebula.
7. **Dual TUN mode** — kernel TUN (real interface, root) for full OS integration; userspace (gvisor, non-root) for restricted environments. Auto-detected based on permissions.

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

## Nebula Vendor Patches

We maintain a numbered series of patches on top of `slackhq/nebula`, applied
in order by `make patch-vendor` (called automatically by `make vendor`). The
canonical inventory lives in [`patches/README.md`](../patches/README.md);
this section just summarizes the architectural shape.

- **Bug fixes (01, 02)** — `os.Exit(2)` on userspace shutdown ([#1031](https://github.com/slackhq/nebula/issues/1031), upstream PR [#1375](https://github.com/slackhq/nebula/pull/1375)) and a nil-pointer panic in the handshake test-reply path. Upstreamable.
- **Allocation hygiene (03)** — caches the Darwin TUN read buffer (~9 KB/packet allocation eliminated).
- **Darwin batch syscalls (04, 06, 07, 08, 12)** — `sendmsg_x` / `recvmsg_x` for UDP and the utun fd, glue to drive them from `listenIn`, and a clean `Flush()` extension to the `udp.Conn` interface. Patch 12 splits `listenIn` into a 2-goroutine pipeline (reader + worker-flusher) that overlaps the two blocking syscalls — see [`docs/macos-pipelined-listenin.md`](macos-pipelined-listenin.md) for the full rationale, profile, and benchmark.
- **Priority queue (09, 10)** — 2-lane control/data queue in the `sendmsg_x` send path so handshakes/lighthouse traffic can preempt bulk data without reordering within a single TCP flow.
- **NAT-PMP advertise (11)** — runtime injection of the public `IP:port` from `internal/portmap/` into the lighthouse's `advertise_addrs`, enabling direct P2P across asymmetric carrier NAT.

- **Apply**: `make vendor` (automatic on first checkout) or `make patch-vendor` (re-apply after re-vendoring).
- **Monitor**: `scripts/check-nebula-patch.sh` watches upstream for the bug-fix PRs landing.
- **Test patches (05, 10)** ship with their corresponding feature patches and run via the standard `go test ./vendor/...` path on Darwin.
