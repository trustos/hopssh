# hopssh — Feature Inventory

*What ships today. No planned features — only what works in the current release.*

---

## Mesh Networking

- **Per-network Nebula overlay** — Each network gets its own Nebula instance with an isolated CA (Curve25519). Networks are cryptographically separated.
- **Persistent lighthouse per network** — Control plane runs an embedded Nebula lighthouse on a dedicated UDP port per network. No customer-hosted infrastructure needed.
- **Persistent relay per network** — Bundled into the lighthouse. Relay is blind (cannot decrypt traffic).
- **Automatic PKI** — CA auto-generated at network creation (10-year validity). Node certificates issued on enrollment.
- **Short-lived certificates** — Node certs expire in 24 hours, auto-renewed by the agent before expiry with jitter.
- **Per-network subnet allocation** — Each network gets a unique /24 subnet. Node IPs allocated via MAX (monotonically increasing).
- **Idle network reaper** — Unused Nebula instances are stopped automatically to conserve resources.
- **Userspace Nebula** — Runs in userspace via gvisor netstack. No kernel TUN device required (optional kernel mode available).
- **Multi-network per agent (v0.10+)** — A single `hop-agent` process joins N networks simultaneously with independent Nebula instances, certs, heartbeats, and split-DNS domains per network. ZeroTier-style "one daemon, many overlays". Cross-network isolation is cryptographic (each network has its own CA). Each enrollment binds Nebula to a unique UDP listen port (4242, 4243, …) so multi-network hosts don't race for the same port.
- **NAT-PMP + UPnP-IGD + PCP port mapping (v0.10.3+/v0.10.4+/v0.10.5+)** — On startup, each enrollment asks the local home router to forward a public UDP port to its Nebula listen port. Tries NAT-PMP (RFC 6886), PCP (RFC 6887), and UPnP-IGD in parallel; first successful response wins. Coverage: NAT-PMP catches Apple-AirPort-style and modern TP-Link/miniupnpd routers; PCP catches newer FiOS-issued and modern Linksys; UPnP catches the much larger pool of consumer routers (Netgear, ASUS, Linksys, Verizon FiOS, ISP-supplied boxes — most home networks deployed since 2005). Combined coverage approaches 100% of consumer routers that allow ANY port-mapping protocol. The resulting `public_ip:public_port` is injected into the lighthouse's `advertise_addrs` (vendor patch 11). Peers — including those behind random-port symmetric CGNAT (cellular) — reach this node directly instead of falling back to relay. Verified end-to-end: cellular-CGNAT MacBook ↔ home-router Mac mini achieves 35-43 ms direct P2P RTT (was 200+ ms via relay).
- **Peer relay (v0.10.5+)** — Designate any node as a relay via the `relay` capability (dashboard toggle: `PUT /api/networks/{id}/nodes/{nid}/capabilities` with `"relay"` in the list). Other agents auto-discover relay-capable peers via the heartbeat response and add them to their `relay.relays` list, so they can use a nearby user-contributed relay (e.g. an EU-based home Mac mini) instead of always going through the central Oracle Cloud lighthouse-relay. Cuts intra-region RTT dramatically and removes the lighthouse as a capacity SPOF. Capability changes take effect on next agent restart or cert renewal (24 h cycle).
- **Birthday-paradox burst-probe primitive (v0.10.5+)** — `internal/burstsock` package implements the simultaneous-burst NAT-traversal technique Tailscale's magicsock uses for the bidirectional symmetric-CGNAT case (both peers cellular). Pool of N source sockets × K candidate destination ports per cycle gives ~63 % collision probability at N=K=256 (sqrt(65536)) per the birthday paradox. Pacing-aware to stay under typical 100 pps carrier rate-limits. Ships as a building block; full Nebula handshake-manager integration (lighthouse-coordinated burst trigger via a new proto message) deferred to a later release that needs a Nebula vendor patch.

## Node Enrollment

- **Device flow (RFC 8628)** — Agent requests a device code, user authorizes in the browser by entering the code and selecting a network. Safest option: no token touches the device.
- **Token-based** — Admin generates a one-time enrollment token (10-minute TTL) from the dashboard. Agent consumes it to join.
- **Bundle-based (offline)** — Admin pre-generates a tarball with CA cert, node cert, key, and config. Single-use download token, 15-minute expiry. For air-gapped environments.
- **Client join** — Authenticated user enrolls their own device (laptop/phone) into a network directly from the API.

## Web Terminal

- **Browser-based PTY** — Full interactive terminal session via WebSocket. xterm-256color, resize support, binary control frames.
- **Through the mesh** — Browser connects to the control plane over HTTPS, control plane reaches the agent through the Nebula mesh. End-to-end encrypted.
- **Per-node capability toggle** — Terminal access can be enabled/disabled per node from the dashboard.
- **Audit logged** — Every shell connection is recorded in the audit log with user, node, and timestamp.

## Port Forwarding & HTTP Proxy

- **TCP port forwarding** — Start a local TCP listener that tunnels through the mesh to a remote node port. Tracks active connections.
- **Browser HTTP proxy** — Access any web service running on a mesh node directly from the dashboard. No VPN client needed.
- **WebSocket proxy support** — Proxied connections support WebSocket upgrade for real-time apps.
- **Service Worker URL rewriting** — Proxy injects a Service Worker to rewrite URLs and force credentials for proxied web apps.
- **Per-node capability toggle** — Port forwarding and proxy can be enabled/disabled per node.

## Node Management

- **Health checks** — Agent reports hostname, OS, architecture, and uptime. Control plane tracks online/offline status via heartbeats (every 60 s, plus out-of-cycle on detected wake/addr-change).
- **Node rename** — Rename a node from the dashboard. DNS records update automatically.
- **Node delete** — Remove a node and its certificates.
- **Capability control** — Toggle terminal, health, and forward capabilities per node from the dashboard.
- **Connection type per node** — Colored badge (P2P / Mixed / Relayed) next to each node's status. Agent reports peer counts (direct vs relay-routed) via heartbeat; server derives the `connectivity` string. Tooltip shows counts + "reported X min ago."
- **Real-time status** — Node online/offline transitions pushed via WebSocket. Dashboard also derives offline status client-side from `lastSeenAt + 180 s` stale threshold — an open tab flips a node to "offline" within <1 s of the threshold crossing without waiting for the next poll.
- **Proxy traffic = liveness** — Successful shell / exec / port-forward / HTTP-proxy interactions refresh the node's `last_seen_at` (30 s per-node throttle). A node whose outbound heartbeat is broken stays marked online as long as user traffic reaches it.
- **Offline UI cues** — Terminal pane shows a red banner + tab dot when its session's node is offline OR the WebSocket is reconnecting/failed. Port-forward rows show a "node offline" pill + muted styling when the target is stale.
- **Node type** — Unified model. All nodes are equal — no server/client distinction. Capabilities determine what each node can do.

## DNS

- **Auto-generated hostnames** — Every node gets `{hostname}.{domain}` resolving to its Nebula IP. Updated on rename.
- **Custom DNS records** — Create arbitrary `{name}.{domain}` → IP mappings per network from the dashboard.
- **User-defined domains** — Each network chooses its own domain (`.hop`, `.prod`, `.lab`, `.zero`, etc.).
- **Per-network DNS server** — Dedicated DNS resolver per network, bound on its own port.

## Teams & Sharing

- **Network sharing** — Share a network with other users. Shared users see the network in their dashboard.
- **Role-based access** — Two roles: `admin` (full control) and `member` (view + use). Network creators are admins.
- **Invite links** — Admins generate invite codes with configurable max uses, expiry, and role assignment.
- **Accept invite page** — Public page showing network name, role, and expiry. Requires authentication to accept.
- **Member management** — Admins can list and remove members from a network.

## Audit Logging

- **Tracked actions** — Registration, login, shell connect, command execution, port-forward start, node delete, and other security-significant operations.
- **Enriched entries** — Each entry includes user name/email, node hostname, network endpoint, and action details.
- **Per-user audit log** — View all your actions across all networks.
- **Per-network audit log** — Admins see all member actions within a network.
- **Batched writes** — Audit log writes are batched to reduce SQLite contention under load.

## Authentication & Security

- **Email/password registration** — Bcrypt hashed (min 8, max 72 characters).
- **Session-based auth** — 64-char hex tokens (crypto/rand), 30-day TTL, HttpOnly cookies with Secure flag.
- **Bearer token auth** — Agents authenticate with encrypted tokens verified via constant-time comparison.
- **Rate limiting** — Public endpoints limited to 10 req/min with 20-burst.
- **CORS** — Same-origin default, configurable via `--allowed-origins`.
- **WebSocket origin validation** — Same-origin check with explicit allowlist.
- **AES-256-GCM encryption at rest** — Agent tokens and sensitive data encrypted in the database.
- **No stored SSH keys** — Control plane never holds SSH keys, cloud credentials, or server passwords.
- **HTTPS enforcement** — Bundle downloads refuse plain HTTP. Secure cookies when behind HTTPS.
- **Enrollment token security** — SHA-256 hashed at rest, single-use, 10-minute TTL, consumed atomically.

## Agent (`hop-agent`)

- **`enroll`** — Join a network (4 modes: device flow, token, stdin, bundle). Flags: `--endpoint`, `--name`, `--tun-mode`, `--no-service`, `--force`, `--config-dir`. Can be run multiple times to join additional networks; rejects same-network duplicates by `(endpoint, CA fingerprint)`.
- **`serve`** — Run the agent service. Loops over all enrollments; one Nebula instance per network bound to its own mesh listener on port 41820.
- **`leave`** — Remove one enrollment (`--network <name>`). Cleans up the subdir, platform DNS registration, and registry entry; restarts the service if other enrollments remain.
- **`install`** — Install as a system service (systemd on Linux, launchd on macOS, Windows SCM on Windows — LocalSystem with auto-restart). Idempotent: re-running restarts the existing service.
- **`uninstall`** — Remove the system service.
- **`status`** — List all enrollments with Nebula IP, cert expiry, groups, endpoint, node ID, TUN mode. Optional `--network <name>` filter.
- **`info`** — Show hostname, DNS name, OS/arch, node ID, endpoint, config location.
- **`update`** — Check for and install updates (from GitHub releases or control plane).
- **`restart`** — Restart the agent service.
- **`stop`** — Stop the agent service.
- **`version`** — Show version and build info.
- **`help`** — Show help.
- **Per-enrollment config layout** — `<configDir>/<enrollment-name>/` holds each membership's cert, key, token, Nebula yaml, DNS config. Top-level `enrollments.json` indexes them. Pre-v0.10 flat layouts auto-migrate on first launch.
- **Non-root support** — Runs in user-level config with userspace TUN mode. No root required.
- **Platform DNS configuration** — Auto-configures split DNS per network: one `/etc/resolver/<domain>` file per domain on macOS; per-link DNS on each instance's Nebula interface (`hop-<name>`) on Linux, with a merged `/etc/systemd/resolved.conf.d/hopssh.conf` fallback; one loopback DNS proxy per enrollment on Windows (`127.53.0.1`, `127.53.0.2`, …) with one NRPT rule per domain.

## Server (`hop-server`)

- **Single binary** — API, web UI, SQLite, lighthouse, relay, and DNS all in one process.
- **Embedded web UI** — Svelte 5 SPA built into the Go binary. No separate frontend server.
- **SQLite database** — Pure Go (no CGO), WAL mode, lock retry with escalating backoff. 20 read connections, 1 write connection.
- **Flags** — `--addr`, `--data`, `--endpoint`, `--lighthouse-host`, `--trusted-proxy`, `--allowed-origins`.
- **Environment variables** — `HOPSSH_ADDR`, `HOPSSH_DATA`, `HOPSSH_ENDPOINT`, `HOPSSH_LIGHTHOUSE_HOST`, `HOPSSH_TRUSTED_PROXY`, `HOPSSH_ALLOWED_ORIGINS`, `HOPSSH_ENCRYPTION_KEY`.
- **`install`** — Install as a system service (systemd/launchd).
- **`uninstall`** — Remove the system service.
- **`update`** — Self-update from GitHub releases.
- **`healthz`** — Health probe (exits 0 if running, 1 if down). Used by container orchestrators.

## Distribution & Updates

- **Dynamic install script** — `GET /install.sh` serves a shell script with the control plane endpoint pre-baked.
- **Self-update** — Both agent and server can update themselves. Downloads from GitHub or control plane, verifies SHA256.
- **Cross-platform releases** — GitHub Actions builds for Linux (amd64, arm64), macOS (amd64, arm64), Windows (amd64, arm64).
- **GitHub Releases** — Version endpoint (`GET /version`) and download redirects (`GET /download/{binary}`).
- **SHA256 checksums** — Published with every release for verification.

## Deployment

- **Docker** — Multi-stage distroless build (gcr.io/distroless/base-debian12:nonroot). Multi-arch (linux/amd64, linux/arm64).
- **Docker Compose** — Local dev setup with mapped ports and volume mount.
- **systemd** — Unit file generation for Linux (agent and server).
- **launchd** — Plist generation for macOS (agent and server).
- **Windows service (SCM)** — `hop-agent install` on Windows registers as a proper Service Control Manager service (LocalSystem, auto-restart recovery, auto-start at boot). Graceful shutdown wired through Stop/Shutdown control codes; logs written to `%ProgramData%\hopssh\hop-agent.log`. Self-update uses rename-swap to replace the running .exe + `sc.exe stop/start` to apply.
- **Non-root agent** — Userspace TUN mode, user-level config directory.
- **Container health check** — `hop-server healthz` every 10 seconds.
- **Port ranges** — TCP 9473 (API/web), UDP 42001-42100 (Nebula per network), UDP 15300-15400 (DNS per network).

## Real-time Events & Activity Log

- **WebSocket event stream** — Per-network WebSocket endpoint pushes events as they happen.
- **Event types** — `node.enrolled`, `node.status`, `node.renamed`, `node.deleted`, `node.capabilities`, `dns.changed`, `member.changed`.
- **Dashboard integration** — Frontend receives events and updates the UI in real-time without polling.
- **Persistent activity log** (v0.9.14) — Every event is also persisted to the `network_events` table so the Activity tab survives page refresh and supports post-hoc debugging. `GET /api/networks/{id}/events/history?since=&type=&limit=` returns latest-first with time-range, type, and pagination (`Load older` cursor).
- **Transition-only `node.status` persistence** — A 30 s background sweeper flips stale → offline and logs exactly one transition event; heartbeats do NOT generate one row per beat. Write rate stays flat regardless of fleet size.
- **Activity tab UI** — shadcn `ui/table/*` + TanStack data-table with sortable columns, type filter pills (All / Status / Enrollment / DNS / Members / Rename / Capability / Delete), free-text search, and time range (24 h / 7 d / 30 d / all).

## Network Visibility

- **Per-node P2P / relay badge** (v0.9.10) — Colored badge per node (Direct / Mixed / Relayed / Idle) derived from agent-reported peer counts in the heartbeat. #1 cross-competitor pain — no competitor surfaces this in the dashboard.
- **Per-peer drill-down** (v0.9.13) — Click a node row to expand an inline table showing every peer with its own direct/relayed classification, mesh IP, and remote address. Asymmetric views (A says direct, B says relayed) render as two rows — genuine diagnostic signal for one-sided hole-punch.
- **Network topology diagram** (v0.9.13, v0.9.14 fixes) — cytoscape + fcose force-directed graph. Nodes colored by status; edges colored by reported connection type. Lighthouses distinguished as diamonds. Pan / zoom / dragged positions preserved across live updates; Recenter button snaps back to fit-all.

## Dashboard Freshness

- **60 s heartbeat + 180 s stale threshold** (v0.9.11) — node flips online / offline within 3 min of a real state change. Wake events on the agent fire a single out-of-cycle heartbeat so dashboards see wake-from-sleep transitions in <5 s.
- **Client-side offline derivation** (v0.9.12) — dashboard derives effective status from `lastSeenAt + 180 s`, so an already-open tab flips a node to "offline" ≤1 s after the threshold crosses without waiting for polling.
- **Proxy traffic as liveness signal** (v0.9.12) — every successful shell / exec / port-forward / health proxy round-trip refreshes `last_seen_at` (throttled 30 s per node). Covers the case where the agent is serving mesh traffic but its heartbeat channel is broken.
- **Responsive tables** (v0.9.14) — Nodes, Peers, DNS, Members, Audit, and Activity tables all use shadcn primitives with breakpoint-driven column hiding (no horizontal scroll at any viewport width).
- **Version visibility** (v0.9.15) — Control plane version shown in the sidebar footer (fetched from `GET /version`); each node's self-reported `hop-agent` version shown in the Nodes table Version column. Drift between control plane and agent is highlighted in amber — makes it obvious when a rollout hasn't propagated or a self-update is stuck.

## Performance & Optimization

- **macOS batch UDP syscalls (`sendmsg_x`/`recvmsg_x`)** — pure Go, no CGO. Batches 32-128 packets per syscall on macOS via private XNU syscalls. Tunnel efficiency 17% → 35-53% of raw WiFi. No other VPN uses these. (Vendor patches 04-08.)
- **macOS TUN batch reads** — `recvmsg_x` on the `utun` device for inbound batching. Companion to batch UDP. (Vendor patch 06.)
- **macOS control-lane priority queue** — 2-lane send queue: Nebula control packets (handshake, lighthouse, test, close) ahead of data. Preserves within-flow ordering for data. (Vendor patches 09-10.)
- **TUN read buffer caching (macOS)** — eliminates per-packet `make([]byte, len+4)` allocation (~9KB/packet). (Vendor patch 03.)
- **AES-GCM cipher default** — exploits Apple Silicon / x86 AES-NI hardware instructions (single-cycle). Faster than ChaCha20-Poly1305 on modern hardware.
- **Tunnel pre-warming** — agent blocks on startup until all peer Noise handshakes complete, ensuring tunnels are ready on first connection.
- **GOGC=400** — reduces Go GC pause frequency 4×, eliminates 100ms latency spikes from garbage collection.
- **pprof endpoint** — built-in CPU/memory profiling at `/debug/pprof/*` behind bearer token auth.

## API

- **REST API** — Full CRUD for networks, nodes, DNS records, members, invites, port forwards, and audit logs.
- **Swagger documentation** — OpenAPI spec served at `/swagger/*`.
- **Heartbeat batching** — Agent heartbeats are batched to reduce database write contention at scale.
- **Audit log batching** — Audit writes are batched for the same reason.
- **MaxBytesReader** — All request bodies are size-limited (1 MB default).

## CI/CD

- **GitHub Actions CI** — Build, vet, test on every push and PR.
- **Cross-compilation** — Linux amd64 + arm64 builds as CI artifacts.
- **Patch verification** — CI checks if the Nebula vendor patch is still needed.
- **Release automation** — Tag-triggered workflow: build 6 platform binaries + multi-arch Docker image + GitHub Release with checksums.
- **Makefile** — `setup`, `build`, `build-all`, `build-linux`, `frontend`, `vet`, `test`, `release`, `patch-vendor`.
