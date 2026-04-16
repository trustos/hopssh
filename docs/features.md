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

- **Health checks** — Agent reports hostname, OS, architecture, and uptime. Control plane tracks online/offline status via heartbeats.
- **Node rename** — Rename a node from the dashboard. DNS records update automatically.
- **Node delete** — Remove a node and its certificates.
- **Capability control** — Toggle terminal, health, and forward capabilities per node from the dashboard.
- **Real-time status** — Node online/offline transitions pushed via WebSocket. No polling.
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

- **`enroll`** — Join a network (4 modes: device flow, token, stdin, bundle). Flags: `--endpoint`, `--tun-mode`, `--no-service`, `--force`, `--config-dir`.
- **`serve`** — Run the agent service. Listens on port 41820 for control plane proxy connections.
- **`install`** — Install as a system service (systemd on Linux, launchd on macOS, Windows service via registry).
- **`uninstall`** — Remove the system service.
- **`status`** — Show enrollment status, Nebula IP, cert expiry, groups, endpoint, node ID.
- **`info`** — Show hostname, DNS name, OS/arch, node ID, endpoint, config location.
- **`update`** — Check for and install updates (from GitHub releases or control plane).
- **`restart`** — Restart the agent service.
- **`stop`** — Stop the agent service.
- **`version`** — Show version and build info.
- **`help`** — Show help.
- **Non-root support** — Runs in user-level config with userspace TUN mode. No root required.
- **Platform DNS configuration** — Auto-configures split DNS (systemd-resolved on Linux, scutil on macOS, registry on Windows).

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
- **Windows service** — Registry-based service installation for the agent.
- **Non-root agent** — Userspace TUN mode, user-level config directory.
- **Container health check** — `hop-server healthz` every 10 seconds.
- **Port ranges** — TCP 9473 (API/web), UDP 42001-42100 (Nebula per network), UDP 15300-15400 (DNS per network).

## Real-time Events

- **WebSocket event stream** — Per-network WebSocket endpoint pushes events as they happen.
- **Event types** — `node.enrolled`, `node.status`, `node.renamed`, `node.deleted`, `node.capabilities`, `dns.changed`, `member.changed`.
- **Dashboard integration** — Frontend receives events and updates the UI in real-time without polling.

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
