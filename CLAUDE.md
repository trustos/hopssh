# hopssh

**Hop into your network. Your servers, your rules.**

Encrypted mesh networking with P2P, relay fallback, built-in DNS, and a web terminal.
The best self-hosted alternative to Tailscale and ZeroTier. Single binary, zero infrastructure.

Website: https://hopssh.com
Domain: hopssh.com
CLI name: `hop`

---

## Product Overview

hopssh is an encrypted mesh networking platform built on Nebula. It creates P2P tunnels
between your devices with automatic relay fallback, built-in DNS resolution, and a web-based
management dashboard — including a browser terminal for server access.

Think ZeroTier/Tailscale, but:
- **Best self-hosted experience** — single binary, embedded web UI, SQLite, zero infrastructure
- **Browser-based web terminal** — SSH into any node from the dashboard (no one else has this)
- **User-defined DNS** — `jellyfin.zero`, `nas.home`, `db.prod` — pick your own domain per network
- **Visual mesh management** — see P2P vs relayed connections, exposed services, DNS records

### What it is
- **Mesh network** — P2P encrypted tunnels between all your devices (Nebula, Noise Protocol)
- **Control plane** (hosted or self-hosted) — manages networks, nodes, PKI, DNS, lighthouse+relay
- **Agent** — installed on servers, joins the mesh, exposes services
- **Client** — installed on laptops/phones, joins the mesh, accesses services
- **Web dashboard** — manage networks, nodes, DNS, port exposure; built-in web terminal
- **Built-in DNS** — `hostname.yourdomain` resolves to mesh IPs, user-defined domains per network

### How it compares

| | ZeroTier | Tailscale | hopssh |
|---|---|---|---|
| P2P mesh | Yes | Yes | Yes (Nebula) |
| Relay fallback | Roots (UDP) | DERP (TCP/443) | Lighthouse relay (UDP) |
| Self-hosted control | Clunky | Headscale (separate) | First-class (single binary) |
| Self-hosted relay | Moons (no UI) | DERP (manual) | Built-in (dashboard config) |
| Web terminal | No | No | Yes (browser-based PTY) |
| DNS | Manual | MagicDNS | User-defined domains |
| Management UI | Hosted only | Limited | Always (embedded in binary) |
| Protocol | Custom | WireGuard | Nebula (Noise, Curve25519) |
| License | BSL | Proprietary | MIT (Nebula) |

---

## Architecture

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

### Connection flows

**P2P (primary, ~92% of connections):**
Agent A asks lighthouse "where is B?" → lighthouse returns B's endpoint → A and B establish direct UDP tunnel via hole punching.

**Relay (fallback, ~8% — symmetric NAT, firewalls):**
Agent A → Lighthouse/Relay → Agent B. E2E encrypted, relay is blind.

**Web terminal (browser → agent through control plane):**
Browser → HTTPS to control plane → Nebula mesh → Agent. Always through control plane since browsers can't join the mesh directly.

### Trust model
- All traffic encrypted end-to-end via Nebula (Noise Protocol, Curve25519)
- Per-network CA — networks are cryptographically isolated (separate Nebula CAs)
- Nodes authenticate with per-node certificates (24h, auto-renewed)
- Agent tokens encrypted at rest (AES-256-GCM), verified with constant-time comparison
- Enrollment tokens SHA-256 hashed, single-use, 10-minute TTL
- Control plane never stores SSH keys, cloud credentials, or server passwords
- Relay is blind — cannot decrypt traffic (just forwards opaque Nebula packets)
- Unified node model — all nodes equal, per-node capabilities (terminal, health, forward)
- Nebula firewall groups (node/admin) control mesh-level access
- If agent stops, control plane loses access — customer always in control

---

## Origin

Core mesh/agent code extracted from [pulumi-ui](https://github.com/trustos/pulumi-ui),
a self-hosted Pulumi infrastructure UI. The following packages are the foundation:

| pulumi-ui package | hopssh equivalent | What it does |
|---|---|---|
| `cmd/agent/` | `cmd/agent/` | Agent binary (health, exec, shell WebSocket, PTY) |
| `internal/mesh/mesh.go` | `internal/mesh/` | Userspace Nebula tunnel manager (on-demand, idle reaper) |
| `internal/mesh/forward.go` | `internal/mesh/` | kubectl-style TCP port forwarding |
| `internal/nebula/pki.go` | `internal/pki/` | CA generation, cert issuance (Curve25519) |
| `internal/api/agent_proxy.go` | `internal/api/` | Health, shell, port forward proxy endpoints |
| `internal/agentinject/bootstrap.go` | `cmd/hop/` + install script | Agent bootstrap (adapted for one-liner install) |
| `internal/crypto/crypto.go` | `internal/crypto/` | AES-256-GCM encryption at rest |

Packages NOT carried over (Pulumi/OCI-specific):
- `internal/engine/` — Pulumi Automation API
- `internal/blueprints/` — YAML blueprint system
- `internal/applications/` — Nomad app deployment
- `internal/oci/` — OCI REST client
- `internal/agentinject/yaml.go` — Pulumi YAML injection

---

## Tech Stack

| Layer | Technology |
|---|---|
| Backend | Go, single binary, `net/http` + chi router, sqlc |
| Database | SQLite via modernc.org/sqlite (pure Go, no CGO), WAL mode |
| Encryption | AES-256-GCM at rest, Nebula/Noise (ChaCha20-Poly1305) in transit |
| Mesh | Nebula (userspace, gvisor netstack), Curve25519 PKI — see Vendor Patch below |
| Frontend | Svelte 5 SPA, embedded in Go binary |
| Auth | Session-based (cookie) + bearer tokens (agents) |
| Container | Distroless (gcr.io/distroless/base-debian12:nonroot) |
| CI/CD | GitHub Actions (build, vet, test, cross-compile, release on tag) |

---

## Vendor Patch: Nebula

We vendor dependencies and apply a 1-line patch to `slackhq/nebula` to fix a critical bug:
upstream `svc.Close()` calls `os.Exit(2)` which kills the entire control plane process.
The root cause is in `interface.go` — the error check matches `os.ErrClosed` but userspace
mode returns `io.ErrClosedPipe`. Our patch adds the missing error check.

- **Upstream issue**: https://github.com/slackhq/nebula/issues/1031
- **Upstream fix PR**: https://github.com/slackhq/nebula/pull/1375 (open, not merged)
- **Our patch**: `patches/nebula-1031-graceful-shutdown.patch` (1 line in `interface.go`)
- **Apply**: `make patch-vendor` (or `make vendor` which vendors + patches)
- **Check script**: `scripts/check-nebula-patch.sh` — run monthly or in CI
- **Build**: use `go build -mod=vendor ./...` (or `make build`)
- **When to drop**: When PR #1375 merges and a new nebula release includes the fix,
  update the version, re-vendor, and remove the patch file

---

## Features & Roadmap

**Phase 1 — Mesh Networking** — complete. Enrollment, web terminal, port forwarding, DNS, teams, audit, real-time events, self-update, Docker. See [docs/features.md](docs/features.md) for the full shipping inventory.

**Phase 2 — Product & Adoption** — in progress. Webhooks, GitHub OAuth, API keys, SSO/OIDC, firewall rules, subnet routing. See [docs/roadmap.md](docs/roadmap.md) for the numbered implementation plan with priorities, complexity estimates, and dependencies.

**Phase 3+ — Enterprise & Scale** — planned. Session recording, RBAC, desktop/mobile apps, regional relays, policy model. See [docs/roadmap.md](docs/roadmap.md).

---

## Pricing Model

### Hosted (hopssh.com)
| Tier | Price | Limits |
|---|---|---|
| Free | $0 | No limit enforced today — target: 25 nodes, 1 network, P2P + relay |
| Pro | $5/node/month | Unlimited networks, DNS, web terminal, audit, API keys |
| Enterprise | Contact | SSO, RBAC, session recording, log streaming, SLA |

### Self-hosted
Free forever. Run the single binary on your own server. All features included.

See [docs/roadmap.md](docs/roadmap.md) for pricing rationale and competitive context.

---

## Project Structure

```
cmd/
  agent/              Agent binary (serve, enroll, install, status, info, help, update)
  server/             Control plane binary (API, lighthouse, relay, DNS, install, healthz)

internal/
  api/                HTTP handlers (auth, networks, nodes, proxy, device, bundles, dns,
                      members, invites, events, distribution)
  auth/               Middleware (session auth, rate limiting)
  authz/              Authorization (CheckAccess — role-based: admin/member)
  buildinfo/          Version + commit (injected via ldflags)
  crypto/             AES-256-GCM encryption at rest
  db/                 SQLite stores + migrations + sqlc generated code
  frontend/           Embedded SPA (built from frontend/)
  mesh/               NetworkManager (persistent per-network Nebula instances, idle reaper)
  pki/                Nebula CA + cert generation
  selfupdate/         Binary self-update from control plane or GitHub

frontend/             Svelte 5 SPA (shadcn-svelte, Tailwind, xterm.js)
patches/              Vendor patches (Nebula graceful shutdown)
scripts/              Maintenance scripts (install.sh)
docs/                 Architecture, enrollment guide, developer guide
deploy/               Deployment templates (OCI, Nomad, install script)

.github/workflows/    CI (build, vet, test, cross-compile) + Release (cross-platform)
Dockerfile            Distroless multi-stage build (control plane only)
docker-compose.yml    Local dev with Docker Compose
Makefile              Build, vendor, patch, frontend, test, release
```

---

## Development

See [docs/development.md](docs/development.md) for the full developer guide.

```bash
# First-time setup (vendor + apply patches):
make setup

# Build everything (frontend + Go):
make build-all

# Run control plane:
./hop-server --endpoint http://localhost:9473

# Enroll a node (interactive device flow):
hop-agent enroll --endpoint http://<control-plane>:9473

# Enroll with token from dashboard:
echo '<token>' | hop-agent enroll --token-stdin --endpoint http://<control-plane>:9473

# Check status:
hop-agent status
hop-agent info

# Docker:
docker compose up --build

# Or manually:
docker build -t hopssh .
docker run -p 9473:9473 -p 42001-42100:42001-42100/udp -p 15300-15400:15300-15400/udp \
  -v hopssh-data:/data -e HOPSSH_ENDPOINT=http://YOUR_IP:9473 hopssh

# Release (production):
make release              # patch bump (v0.1.0 → v0.1.1)
make release BUMP=minor   # minor bump (v0.1.1 → v0.2.0)
# Tags the version, pushes to GitHub → CI builds binaries + Docker image →
# ghcr.io/trustos/hopssh:<tag>. Then update the Nomad job in oci_nomad_cluster
# repo to use the new tag — GH runner on the server picks it up and deploys.
# Nomad job: /Users/tenevi/Projects/Github.Trustos/oci_nomad_cluster/jobs/hopssh.nomad.hcl
```

### Deployment

**Production release** (control plane + agents):
1. `make release` — tags + pushes → CI builds Docker image + binaries
2. Update `oci_nomad_cluster/jobs/hopssh.nomad.hcl` with new tag → commit + push
3. GH runner deploys the new image via Nomad on Oracle Cloud (arm64)
4. Agents self-update from the control plane

**Dev deploy** (local testing only):
```bash
make dev-deploy            # Build + deploy agent to both local Macs (~12s)
make dev-deploy-server     # Build Docker image → push ghcr.io → update Nomad job
```

- `dev-deploy` deploys to Mac mini (local) + laptop (192.168.23.18) via SCP
- `dev-deploy-server` builds `linux/arm64` Docker image, pushes with `dev-<commit>` tag,
  updates Nomad job in `oci_nomad_cluster` repo, commits + pushes
- Server runs on Oracle Cloud behind Traefik (hopssh.com), no SSH access (NSG blocked)
- Server architecture: arm64, Nomad + Docker, distroless container

---

## Discovery Log

When a major discovery is made during development — something non-obvious about how
the system works, a platform limitation, a performance finding, or a technique that
did/didn't work — write it down here immediately. These save future sessions from
repeating the same investigations.

### Nebula Internals
- **Nebula's hot path is clean** — no goroutine handoffs, no channels, zero per-packet allocations. Buffers pre-allocated per routine. Crypto inline. Don't try to "optimize" the packet processing loop — it's already optimal.
- **`f.outside` is the primary UDP conn** — `f.writers[]` are only used for `routines > 1`. When wrapping UDP connections (e.g., for FEC), must wrap `f.outside` + `f.handshakeManager.outside` + `f.writers[]` for full coverage.
- **Relay overhead is only 9ms** — 2 AEAD operations (verify + re-authenticate) + 2 syscalls. The bottleneck for relay performance is network path, not lighthouse processing.
- **`RebindUDPServer()` exists** but doesn't auto-detect network changes. The agent must poll for changes and call it. Also must `CloseAllTunnels(true)` to force re-handshake on the new network — rebind alone only re-advertises.
- **Tunnels go dead after ~15s of inactivity** — Nebula's connection manager sends test packets and marks tunnels dead if no response. Subnet scanning (254 IPs) to keep tunnels warm floods the handshake manager causing EAGAIN. Use heartbeat-driven peer warmup instead.
- **Nebula tunnels SURVIVE 30s network outages cleanly** — verified empirically (2026-04-16) on Mac mini ↔ MacBook Pro (cellular hotspot, lighthouse-relayed) with `sudo ifconfig en0 down` for 30s. **TCP connections through the tunnel survived the entire outage (0 bytes application data lost — TCP send buffer queues during outage, retransmits on recovery, all 215 of 215 attempted MSGs delivered through a single long-lived `nc` pipe).** ICMP recovery time after `en0 up`: ~3 seconds (first successful ping at T+3 after the network restored). The "~15s tunnel-dead" note above describes idle-detection behavior; *active* tunnels with traffic survive much better. Existing `cmd/agent/nebula.go:147-174` (`watchNetworkChanges` 5s polling + `RebindUDPServer` + `CloseAllTunnels(true)`) is sufficient for real-world mobile reliability — Phase 1 QUIC integration deemed unnecessary for outage survival. Evidence at `spike/nebula-baseline-evidence/`.
- **`preferred_ranges` is essential** — without it, Nebula sorts public IPs before private ones, causing same-NAT peers to try hairpin NAT (fails on most routers) before trying LAN addresses.

### macOS Platform
- **Screen Sharing HP first-click bug is fundamental to userspace utun on modern macOS — not fixable via SystemConfiguration.** (2026-04-20, superseding the earlier SC-fix discovery from 2026-04-19.) The cold-path failure ("Change the screen sharing type to standard and try again") is triggered by `nw_interface_create_with_name("utun11")` failing inside `avconferenced` + `symptomsd`. Log evidence during a fresh-agent HP-first-click failure: `symptomsd: -[NWInterface initWithInterfaceName:] nw_interface_create_with_name(utun11) failed` → `avconferenced: VCTransportSessionSocket initializeInterfaceTypeWithSocket: Not setting unexpected transport type 0` → `avconferenced: localInterfaceType=(null) remoteInterfaceType=(null)` → 4× `VCMediaStream checkRTCPPacketTimeoutAgainstTime: Last RTCP packet receive time: nan` → `_RTPTransport_ReinitializeStream` for video fires 5 s after audio, tripping the RTCP watchdog. The SC-fix plan (`docs/macos-hp-screen-sharing-fix.md` in its original form) attempted to fix this by writing `Setup:/Network/Service/<UUID>/...` keys to SCPreferences, then to SCDynamicStore — three full iterations, including matching Tailscale's live-store shape verbatim (`PrimaryRank=Never`, `ServiceIndex=100`, `UserDefinedName=hopssh-*`). **Every iteration empirically failed** with the identical cold-path signature. `ifconfig -v` reveals the real delta: Tailscale's utun has `xflags=IS_VPN` plus three registered kernel interface agents (`Skywalk NetIf`, `Skywalk FlowSwitch`, `NetworkExtension VPN`) — all set by `NEPacketTunnelProvider` during NE tunnel bring-up, requiring an Apple Developer ID + NE entitlement. The public `SCNetworkInterfaceForceConfigurationRefresh()` API would be the only public signal that could plausibly refresh avconferenced's classifier, but `SCNetworkInterfaceCopyAll()` **does not return** our userspace utun — there's no `SCNetworkInterfaceRef` to call refresh on. Every other userspace VPN on macOS (wireguard-go, Tunnelblick/OpenVPN, strongSwan, NetBird, Nebula itself) faces this identical limitation; ZeroTier sidesteps it architecturally by using `feth` (fake Ethernet) instead of utun, getting ~2/3 HP success vs 0/3 for raw utun. **Conclusion:** no code fix exists without NEPacketTunnelProvider (requires Apple Developer ID, code signing, NE entitlement, and a signed macOS GUI container app). Mitigation: users retry HP after ~10 s (empirically works, reason unknown — possibly kernel interface enumeration latency). Future work: feth-based Nebula vendor patch (L2 layer swap, non-trivial, partial fix); NE bundle (correct long-term path, months of work). Do not reopen the SC-registration angle — it is definitively not the right layer.
- **utun read allocates per-packet** — upstream Nebula's `tun_darwin.go` does `make([]byte, len+4)` on every Read. Our vendor patch caches this buffer. (~9KB allocation eliminated per inbound packet.)
- **macOS `sendmsg_x`/`recvmsg_x` batch syscalls — both sides shipped** — Pure Go (no CGO). `recvmsg_x` works on the utun fd directly (it's `SOCK_DGRAM`/`AF_SYSTEM`), letting us batch TUN reads via the same syscall as UDP receives. Architecture: opportunistic batching — listenIn does blocking first read via Go netpoller, then non-blocking drain via recvmsg_x; UDP send queue flushed via single sendmsg_x after each batch. **No timer** (timer-based flush hurts TCP). Caller-driven flush (listenIn after TUN batch, ListenOut after UDP batch). Mutex protects the send queue (handshake mgr + lighthouse + listenIn + listenOut all produce). Tunnel efficiency went from 17% to 35-53% of raw WiFi.
- **Inline packet prioritization — control-only, NOT size-based** — `sendmsg_x` processes the msghdrX array in order, giving us strict priority at the syscall level for free. We tried a 3-lane size-based split (interactive/realtime/bulk by packet size) — it tanked TCP throughput by 70% (320→96 Mbps) and doubled retransmits because splitting data packets by size reorders TCP segments within a single flow. Replaced with a 2-lane control-only design: only Nebula control packets (type != Message) jump the queue; ALL data packets share one FIFO lane to preserve within-flow ordering. This gives priority to handshakes/lighthouse/keepalives without harming TCP. Classification is one read (`b[0]&0x0f` of plaintext Nebula header). Honest result: throughput preserved (no regression), but ping-under-load improvement is within WiFi noise — most of that latency is WiFi MAC contention, not the VPN queue. Lesson: priority queues inside VPNs MUST preserve within-flow ordering. Tested with TCP retransmit count + mixed-workload throughput, not just ICMP ping.
- **Linux `sendmmsg` with batch-flush HURTS single-stream performance** — 408ms vs 125ms relay by holding packets. Needs per-packet flush architecture.

### Linux Platform
- **systemd-resolved per-link DNS with non-53 ports is broken across the Ubuntu LTS line.** Verified 2026-04-17 on Ubuntu 25.10 (systemd 257.9, aarch64), and 2026-04-18 on Ubuntu 24.04.4 LTS (systemd 255.4, x86_64). `resolvectl dns <iface> <ip>:<port>` + `resolvectl domain <iface> ~<domain>` registers correctly — `resolvectl status` shows the DNS on the right link — but the stub at `127.0.0.53` silently drops `.<domain>` queries. Direct `dig @<ip> -p <port>` works fine. Public DNS via uplink still works. Also reported against older systemd in [NetBird #3443](https://github.com/netbirdio/netbird/issues/3443), so this is NOT a v257+ regression — the bug spans multiple systemd versions. Tailscale avoids the bug entirely because MagicDNS runs on port 53. Fix in `cmd/agent/dns_linux.go`: probe stub after per-link registration, fall back to drop-in `/etc/systemd/resolved.conf.d/hopssh.conf` with `[Resolve] DNS=<ip>:<port> Domains=~<domain>` + `systemctl reload-or-restart systemd-resolved`. Self-diagnostic log: `"per-link DNS registered but stub not forwarding queries; switching to systemd-resolved drop-in config"`. Load-bearing across Ubuntu LTS — not a one-version workaround.
- **QEMU ARM on Apple Silicon cannot test real OS-level sleep/wake.** Three failure modes observed (UTM, Ubuntu 25.10 aarch64 guest): `rtcwake -m mem` rejects with "set rtc wake alarm failed: Invalid argument" (rtc-efi doesn't support alarms); `systemctl suspend` enters S3 but VM **cold-reboots** on wake (journal shows new boot ID; `uptime` confirms fresh boot); `echo freeze > /sys/power/state` hangs the VM indefinitely. For VM-based sleep/wake functional tests of the agent, use SIGSTOP/SIGCONT on the agent process — this exercises the Go runtime's tick-gap detection (`cmd/agent/nebula.go`) without needing real suspend. Caveat: SIGSTOP doesn't exercise interface down/up, kernel DNS reset, or TUN driver pause — those dimensions are untestable in QEMU ARM VMs and need bare-metal Linux.
- **First observation of the `sleep/wake detected` log line came from Linux** (via SIGSTOP), not macOS. On macOS (WiFi), the `addrChanged` branch always wins in `cmd/agent/nebula.go:183-187` because WiFi re-associate changes local addrs during sleep; the log message `"sleep/wake detected (tick gap Ns)"` is masked by `"network change detected"` even though the same rebind code runs. On Linux (enp0s1, no re-associate event during SIGSTOP), the `sleptAndWoke && !addrChanged` branch is reachable and the sleep-specific log fires. Same holds for Windows wired Ethernet. Functionally equivalent; diagnostically useful — if you want to verify the sleep path is firing, Linux or a wired macOS/Windows is the cleaner test.

### Windows Platform
- **NRPT silently strips ports, fixed via local DNS forwarder.** Verified 2026-04-18 on Windows 11 Home 25H2 ARM64. `Add-DnsClientNrptRule -NameServers <ip>:<port>` drops the `:port` suffix — Windows DNS client then queries `<ip>:53` (nothing there, our lighthouse is on :15300) and mesh hostnames time out. Fix shipped in `cmd/agent/dnsproxy_windows.go`: a miekg/dns-based UDP+TCP forwarder on `127.53.0.1:53` forwards queries to the real upstream port. NRPT registers the loopback IP (no port needed — NRPT default is 53, which the forwarder honors). Post-fix: `Resolve-DnsName <host>.home` works (A record, <10ms); `ping <host>.home` from Windows hits the mesh in 4-6ms. Public DNS unaffected (uses uplink). Pre-fix cleanup: `removeHopsshNRPTRules()` now runs on every start to clear stale Comment-tagged rules from previous sessions (no more 4-duplicate NRPT rules accumulating). Proxy binary grew ~10MB (miekg/dns dependency) — acceptable trade for working mesh DNS on Windows.
- **SCM integration — shipped post-v0.9.9.** The previous "not SCM-compatible" state (agent exited with error 1053 under `sc.exe start`) was fixed by adding `cmd/agent/service_windows.go`: `svc.IsWindowsService()` check at the top of `runServe`, goroutine running `svc.Run(agentServiceName, handler)` that translates `SERVICE_CONTROL_STOP/SHUTDOWN` into the same `context.Cancel` the SIGTERM handler uses. `hop-agent install` creates the service via `svc/mgr` with BinaryPath embedding `serve --config-dir <configDir>`, LocalSystem principal (needed for WinTun), auto-restart recovery (5s/5s/30s). Log output redirected to `%ProgramData%\hopssh\hop-agent.log` on service launch (SCM discards stderr otherwise). Verified end-to-end: install → Running, graceful stop logs `Shutting down agent... Goodbye`, restart recovers mesh <5s, uninstall clean. Gotcha: `mgr.Config.BinaryPathName` is silently ignored — `mgr.CreateService` composes the BinaryPath from `(exepath + args...)`, so pass the serve subcommand + flags as trailing CreateService args, not in Config.BinaryPathName.
- **Process detachment fragility is moot once SCM works.** Prior `Start-Process -WindowStyle Hidden` and `schtasks` detach experiments both died within seconds — root cause was never isolated but it's no longer blocking: users install as a proper service instead. If someone does need foreground-like runs (debugging), run from an interactive console/SSH session as before.
- **Windows self-update rename-swap trick.** Windows can't overwrite a running .exe (ERROR_SHARING_VIOLATION on the second arg of `MoveFile`), but it DOES allow renaming a running .exe to a sibling name — the running process keeps its handle open, and the original path becomes free for the new binary. `internal/selfupdate/selfupdate.go:Apply` on Windows renames `<exe>` → `<exe>.old` before placing the new binary, then `sc.exe stop/start` cycles the service. `cleanupOldBinary()` in `service_windows.go` removes the `.old` file on the next start.
- **QEMU ARM Windows on Apple Silicon has NO sleep states available.** `powercfg /a` reports S1, S2, S3, Hibernate, and S0-LPI all unavailable. Same limitation as QEMU ARM Linux. For VM-based sleep/wake functional tests, use `NtSuspendProcess` / `NtResumeProcess` (via P/Invoke in PowerShell) — the Windows SIGSTOP equivalent. Exercises the agent's time-jump detection path but not kernel-level sleep behaviors (which are untestable on these VMs).
- **ConPTY requires `STARTF_USESTDHANDLES` with zero stdio handles to not leak child output to the parent's inherited console.** Verified 2026-04-18 (v0.9.8→v0.9.9) via the `spike/windows-shell-test` harness. Symptom: `CreateProcess` + `PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE` partially works — setup ANSI sequences, title changes, and focus-mode bytes flow through the pseudoconsole pipe, but cmd.exe's PROMPT and any data written via regular stdout bypass ConPTY and land in the agent's parent stream (captured empirically — `yavor@WIN-...>` printed inline in the agent's stderr log file). Fix in `cmd/agent/shell_windows.go`: set `StartupInfoEx.StartupInfo.Flags |= windows.STARTF_USESTDHANDLES` (leave `StdInput/StdOutput/StdError` at zero — the pseudoconsole attribute substitutes them). Without the flag, Windows falls back to routing the child's stdio through the parent's inherited console handles instead of the ConPTY pipes. Matches [UserExistsError/conpty](https://github.com/UserExistsError/conpty)'s pattern; Microsoft's own docs don't document this requirement. Counterintuitive because setting USESTDHANDLES with null handles would normally fail, but PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE writes in the pipes during CreateProcess — so the "null" handles never actually get consulted.

### Performance
- **FEC hurts on bandwidth-constrained paths** — 20% extra parity packets cause congestion-induced loss on cellular (+57% latency). FEC only helps random loss on high-bandwidth paths.
- **Symmetric NAT with random ports is unsolvable BIDIRECTIONALLY (refined 2026-04-20)** — port prediction (±50) doesn't work when the carrier assigns random ports; this remains true for the bidirectional case (both peers in carrier CGNAT). **The asymmetric case is solved as of v0.10.3+** via native NAT-PMP (RFC 6886) port mapping in `internal/portmap/`: the home-router peer asks its UPnP/NAT-PMP-capable router to forward a public UDP port; the resulting public `IP:port` is injected into the lighthouse's `advertise_addrs` via vendor patch 11; the cellular peer (even behind random-port symmetric CGNAT) initiates outbound to that stable public endpoint and the CGNAT's outbound flow mapping carries the response. Empirically verified on Mac mini (TP-Link UPnP, public 46.10.240.91) ↔ MacBook Pro (Yettel BG cellular, symmetric NAT): 35-43ms direct P2P RTT, down from 100% loss / relay fallback. The "no VPN can do this" claim was wrong — Tailscale's been doing it via NAT-PMP since v1; we just hadn't shipped the protocol client.
- **AES-GCM is faster than ChaCha20 on Apple Silicon** — dedicated hardware AES instructions (single-cycle) vs NEON vector ops. Keep `cipher: "aes"`.
- **Large UDP socket buffers cause bufferbloat** — 2MB buffers caused 50-293ms spikes on macOS because it reads one packet at a time (no recvmmsg). OS defaults (~128KB) are correct.

### Dashboard freshness architecture
- **Heartbeat writes coalesce in a sync.Map + 5 s flush**, so tighter client cadence costs ≈ zero. `internal/db/nodes.go` buffers every `RecordHeartbeat` into a `sync.Map` keyed by nodeID; `StartHeartbeatFlusher` writes the whole batch once per 5 s in a single SQLite transaction. 5 × more heartbeats per minute = 5 × bigger batches, same number of transactions. Don't pre-optimize by keeping heartbeats rare — the architecture already absorbs it.
- **Proxy traffic is a liveness signal we can piggyback.** `RecordProxyActivity(nodeID)` (added v0.9.12) fires from every successful shell/exec/health/HTTP-proxy/WebSocket-proxy interaction, throttled per-node at 30 s via `lastProxyHeartbeat sync.Map`. Pattern: when the agent has an independent "alive" channel (proxy round-trip over mesh), use it to backstop its self-reported heartbeat. Catches the failure mode where the agent can serve requests but its outbound heartbeat is broken (DNS, TLS glitch, firewall).
- **Dashboard status needs BOTH server-side and client-side derivation** to feel fresh. Server's `effectiveStatus()` (`internal/api/types.go`) handles fresh API loads. Frontend's `displayStatus(node, now)` (`frontend/src/lib/node-status.ts`) mirrors the same logic against a 1 s `now` ticker so an already-open tab flips to "offline" within <1 s of the 180 s stale threshold — no polling dependency. Must-match: if you change `nodeStaleThreshold` on the server, update `NODE_STALE_THRESHOLD_SEC` on the client. Comments in both files point at each other.
- **Event-driven out-of-cycle heartbeat beats cadence tuning.** `cmd/agent/nebula.go` exposes a buffered-1 `heartbeatTrigger` channel; `watchNetworkChanges` sends on it whenever it fires the rebind block (tick-gap or addr-change). `runHeartbeat`'s select adds a third case that drains the scheduled timer and fires immediately. Wake → dashboard latency is <1 s regardless of where the scheduled tick landed. `signalHeartbeat()` is non-blocking so WiFi flap can't pile up signals.
- **Persist activity events on transitions, not heartbeats (v0.9.14).** `internal/db/network_events.go` mirrors the `audit.go` buffered-flush pattern (2 s, 100-entry batch, drop-on-overflow) into the `network_events` table. Every `EventHub.Publish` site in `internal/api/{enroll,device,renew,proxy}.go` calls `NetworkEvents.Record(...)` alongside the WS push — except `node.status`, which is filtered through `NodeStore.lastLoggedStatus sync.Map` so only online↔offline transitions land. A `StartStatusTransitionSweeper` goroutine (30 s) flips stale online rows to offline and emits exactly one transition event. Result: row rate stays ~linear in real state changes, not heartbeats. The Activity tab reads via `GET /api/networks/{id}/events/history?since=&type=&limit=` and merges with the live WS ring buffer on the client.
- **macOS dev-deploy xattr gotcha (v0.9.14 script fix).** `sudo cp` from the build directory into `/usr/local/bin/` carries xattrs forward; launchd's AMFI then refuses to load the adhoc-signed LaunchDaemon with `OS_REASON_CODESIGNING` and the agent silently loops on spawn failures. The laptop deploy path works because `scp` strips xattrs in transit. Fix in `scripts/dev-deploy.sh`: use `sudo /usr/bin/ditto --noextattr` for the local install. Don't try `codesign --force --sign -` — re-adhoc-signing a Go-linker-signed binary corrupts something in its runtime data and it panics in `bart.Table.Insert` on Nebula allow-list parsing on startup.
- **Nebula's hostmap can hold MULTIPLE HostInfo entries per peer during transitions (fixed v0.10.6).** When a tunnel transitions from relay-routed to direct (Pillar 1 NAT-PMP fix activates, lighthouse propagates the new public addr, peer initiates direct handshake), the OLD relay-session HostInfo lingers in `HostMap.Hosts` until `connection_manager`'s next sweep prunes it (~90 s). During that window, `ListHostmapHosts` returns BOTH entries for the same peer. Pre-fix `collectPeerState` walked the slice and emitted one PeerDetail per HostInfo — so the heartbeat shipped "1 direct, 1 relayed" for the SAME peer, and the dashboard rendered "Mixed" even though traffic was already flowing direct. **Fix in `cmd/agent/peerstate.go`:** merge HostInfos by VpnAddr first, then classify; ANY valid `CurrentRemote` makes the peer direct (matches Nebula's actual data-path preference). Same heartbeat shape, same DB writes, no protocol change. Lesson: **never assume a Nebula hostmap iterates one entry per peer** — multiple coexisting sessions are the norm during NAT-traversal transitions, and reporters that don't dedup will show mid-transition states as permanent.

### Multi-network per agent (roadmap #29, shipped v0.10.0)
- **Nebula vendor package is cleanly instance-safe.** `nebula.Main()` returns a self-contained `*Control`; logger is injected; no package-level mutable state. Core structs (`Interface`, `HostMap`, `LightHouse`, `Firewall`, `HandshakeManager`) are heap-allocated per-instance. Our 10 vendor patches are all per-struct. **Only soft issue:** `go-metrics` uses static counter names, so N instances share one counter — acceptable for v1 (per-instance tagging via `logrus.WithField` disambiguates logs; metrics aggregate). Running two `nebula.Main` goroutines in one process Just Works; the refactor cost was all agent-side plumbing, none in the protocol.
- **Server contracts were already pinned for multi-agent.** One heartbeat POST = one nodeID = one network; no server changes needed for #29. The `nodes.peer_state` blob schema comment in `003_node_peer_state.sql` explicitly flagged this. Adding a network to an agent = issuing a fresh enrollment token, nothing else.
- **Migrated `nebula.yaml` keeps stale PKI paths — fix in ensureP2PConfig, not migrate.** When the per-enrollment subdir layout lands, `nebula.yaml` gets renamed into `<configDir>/<name>/nebula.yaml` but its internal `pki.ca/cert/key` fields still point at the flat-layout paths. Nebula opens the stale path, fails, and the agent silently falls back to OS stack. Fix (`cmd/agent/renew.go` in `ensureP2PConfig`): unconditionally rewrite `pki.ca/cert/key` to match `inst.dir()` on every boot — idempotent for correct configs, self-healing for drifted ones. Don't try to fix it in the migration step only; a later rename or move would drift it again.
- **Linux kernel TUN needs a unique dev name per enrollment.** macOS ignores `tun.dev` (kernel auto-allocates `utunN`), so single-network configs can all say `dev: nebula1` with no collision. On Linux a second instance trying to create `nebula1` gets EEXIST from the kernel and the instance dies — silently, because the agent's fallback path catches it. Fix: emit `dev: hop-<enrollment>` in `writeNebulaConfig`, truncated to IFNAMSIZ=15. `ensureP2PConfig` normalizes legacy `dev: nebula1` on next boot (renames the interface once, costs one DNS re-register). `dns_linux.go`'s `configureViaResolvectl` derives the per-instance interface from the same formula — can't call `findNebulaInterface()` first-match once there are two.
- **Same-network duplicate must be rejected by `(endpoint, CA fingerprint)`.** The local name collision check (`chooseEnrollmentName` auto-suffixes to `work-2`, `work-3`) isn't enough — it only notices if the NAME matches. If the user fetches a token for a network they're already in and enrolls, they create a duplicate node on the server with a different local name, paying twice for no benefit. Fix: `existingEnrollmentForNetwork(reg, endpoint, caFingerprint)` in `cmd/agent/enroll.go`; call it in `installCerts` and `enrollFromBundle` before writing anything. Caveat: the check fires AFTER `POST /api/enroll` responds, so the server has already issued a cert and consumed the token — the orphan node has to be cleaned up by hand. We can't move the check earlier without knowing the network's CA fingerprint ahead of time, which requires a server API change (out of scope for v0.10).
- **Windows `hop-agent install` must be idempotent, not error on re-install.** Linux `systemctl enable --now` and macOS `launchctl bootstrap` both succeed on an already-installed service; Windows `mgr.CreateService` errors with "service already exists". Second-enroll flow triggers `installService()` → fails → the new enrollment is on disk but the running agent never re-reads `enrollments.json` until manual `sc.exe restart`. Fix: in `installAgentWindows`, if `OpenService` succeeds on the existing name, `Control(svc.Stop) → wait → Start()` and return cleanly. Matches the stop-start flow the user would do by hand.
- **Per-network lighthouse cold-start is ~60-90 s on a freshly-created network.** Empirically observed during Phase E: after the user created the `work` network in the dashboard, the first node's enrollment handshakes to the work lighthouse (port 42002) mostly timed out (sub-1 % success) for ~90 s, then suddenly stabilized and peer discovery became reliable. Home lighthouse on 42001 was healthy the whole time. Likely a warm-up / cache-cold / lazy-listener effect on the control plane. Agents recover without intervention once the lighthouse stabilizes — but note the symptom when helping users bring a brand-new network online.
- **Phase E caught three real bugs across four hosts.** Kernel-TUN naming (Linux-specific, would have shipped broken on Linux), PKI-path-after-migration (all platforms, would have shipped with agent-silently-degraded), Windows install idempotency (Windows-only UX papercut). Across ~4 hours of multi-platform smoke testing on Mac mini + MacBook + Ubuntu 25.10 ARM64 VM + Windows 11 ARM64 VM. Lesson: per-platform E2E testing is load-bearing before ship — unit tests + cross-compile green doesn't catch runtime-interaction bugs on hardware you don't own.
- **Post-ship gap-analysis caught a goroutine leak the tests would never find (v0.10.1).** Two parallel code-review passes after v0.10.0 flagged that `meshInstance.cancel` was declared but never assigned, and `watchNetworkChanges` had no `ctx.Done()` branch — so every `hop-agent leave` or cert-renewal reload leaked a goroutine that kept calling `RebindUDPServer` / `CloseAllTunnels` on a stopped Nebula Control. The unit tests never caught it because the goroutine's behavior against a dead ctrl was implementation-defined (no-op on today's Nebula, undefined per API). Fix: `meshInstance` now owns `parentCtx` + `watcherCancel`; `startWatcher(ctrl)` cancels any prior watcher and spawns a fresh one on the live ctrl; `stopWatcher()` runs in `close()` and before every hot-restart swap. Lesson: shipping a multi-lifecycle refactor warrants a separate "audit every goroutine's stop condition" pass — unit tests with mocks happily let a leak slide.
- **Recovery-on-corrupt beats log.Fatalf (v0.10.2).** Two low-probability but bricking failure modes had no recovery path in v0.10.0: a crash mid-`migrateLegacyLayout` (half-moved legacy files, agent's fresh-install early-exit strands the registry empty on next boot) and a truncated `enrollments.json` (caller `log.Fatalf`s, agent can't start any enrollment). v0.10.2 adds (a) `detectHalfMigratedSubdir` + `completeMigration` in `legacy_migrate.go` — scan for a single valid-name subdir containing `node.crt`, finish the move, register; and (b) `<configDir>/enrollments.json.bak` written on every successful save, fallback load path that logs loudly and keeps going. Pathological states (both top-level legacy AND a populated subdir, multiple candidate subdirs, both main+backup corrupt) surface as operator-actionable errors. Lesson: for any "index file that drives startup", write a sibling `.bak` after every save and try it on parse failure — one extra atomicWrite per save, turns `log.Fatalf` into a one-save-behind recovery.
- **N-instance-per-network IS Nebula's canonical model, not a workaround.** Defined Networking's own [multi-network guide](https://www.defined.net/blog/multiple-networks/) says "run a second dnclient... configure different interface names (`tun.dev`), or give them tags that configure them for you" — bit-for-bit what `cmd/agent/instance.go` + per-enrollment `dev: hop-<enrollment>` do. Single-instance multi-overlay has been requested upstream since 2020 ([slackhq/nebula#235](https://github.com/slackhq/nebula/issues/235), [#251](https://github.com/slackhq/nebula/issues/251), [#306](https://github.com/slackhq/nebula/issues/306)) with no implementation. If a future reader wonders "should we consolidate into one Nebula instance?" — no. The blockers are in Nebula's trust model (per-overlay lighthouse subnet check, per-instance firewall scope), not in our code. Don't reopen this.
- **Nebula's CAPool multi-CA is for CA rotation/migration only, NOT for joining multiple overlays.** `pki.ca` accepts a PEM bundle of multiple CAs (`vendor/github.com/slackhq/nebula/cert/`), and it's genuinely useful — dual-CA overlap enables v1→v2 cert cutover (roadmap #19) and lets a rotated CA coexist with the old one during renewal drain (roadmap #19a). But pointing one instance at lighthouses with different subnet ranges via CAPool fails with `"lighthouse host is not in our subnet"` — the subnet check is per-instance, so the non-primary overlay's lighthouse gets rejected. If someone proposes "just add the second network's CA to the bundle," that's this pit.
- **Multi-enrollment UDP listen-port collision was silently breaking direct P2P (fixed v0.10.3).** Pre-fix code in `cmd/agent/enroll.go` set `listenPort = nebulacfg.ListenPort` for the first enrollment and `listenPort = 0` (kernel-assigned random) for every subsequent one — to "avoid binding two instances on the same port." This was wrong on two axes. (1) On every restart the random port changed, so the lighthouse never built a stable host-update for the secondary enrollment — peers querying for it received `udpAddrs=[]` and handshakes timed out forever (we observed this for 24+ hours on the user's two-network host before the diagnostic). (2) Random ports defeat NAT-PMP/UPnP entirely — the public mapping lives at `external_ip:external_port`, but `external_port` only exists relative to the internal port we asked for, and the internal port shifted on each restart. **Fix:** new `Enrollment.ListenPort` field, `enrollmentRegistry.NextAvailableListenPort(base)` scans 4242, 4243, 4244 … and persists; `migrateListenPorts` runs at boot to assign + heal nebula.yaml for legacy enrollments. After deploy on the user's host, both enrollments bound their own port (4242 + 4243), each got its own NAT-PMP mapping, and the previously-unreachable home-network MBP came up to 35-43ms direct P2P from cellular within 30 s of lighthouse propagation. Lesson: "let the kernel pick a random port" is acceptable for outbound-only sockets, NEVER for sockets that act as the destination for inbound NAT traversal — the port has to be deterministic AND stable across restarts or the entire P2P story falls over.

### Engineering Lessons (rules for future work)

These are rules learned from real failures. Apply them to any new perf/networking work.

- **Priority queues inside a VPN MUST preserve within-flow ordering.** Splitting packets by size, content, or any flow-internal property reorders TCP segments → SACK fires → TCP treats it as congestion → throughput collapses. We measured: a size-based 3-lane PQ dropped bulk throughput from 320→96 Mbps (-70%) and doubled retransmits (168→383). Only safe to prioritize across orthogonal categories (e.g. control vs data) where reordering can't happen within a single flow.
- **ICMP ping is the wrong benchmark for "real-world latency".** It's UDP, single-packet, no congestion control. Optimizations that improve ping can simultaneously HURT TCP (the user's actual workload). Always measure with: TCP retransmit count, mixed-workload bulk throughput, and TCP-RTT under load (TCP connect time to a closed port works as a probe).
- **Timer-based send batching breaks TCP.** Any flush mechanism that holds packets for a fixed time (we tried 500μs) adds jitter that TCP congestion control reads as congestion. Caller-driven flush (after a TUN-read batch or UDP-recv batch) is fine; timer-driven is not. We measured: 500μs timer dropped throughput from 154→63 Mbps.
- **Trust the user's instinct over synthetic benchmarks.** Three times in this codebase: user reported "feels worse," synthetic numbers said "improved," and the user was right each time — proper measurements (TCP retransmits, throughput, A/B-with-real-workload) confirmed issues hidden by the synthetic test. If the user says it's worse, find the metric that captures it before defending the change.
- **Don't fork Nebula.** Considered, then rejected. Differentiation lives in product features (web terminal, DNS, dashboard, browser proxy, control plane), not in the protocol layer. Patches 01-10 add ~700 lines on top of upstream Nebula; that's the right scope. A fork would be 6-12 months to feature parity + permanent maintenance of crypto code.
- **macOS UDP SO_SNDBUF default is 9216 bytes (~6 packets) — already very small.** Tuning it does nothing for latency. We tested 4KB / 32KB / 128KB / 512KB across 60s probes during real screen sharing: p50/p95/p99 are identical at all sizes. The kernel UDP send buffer is NOT where bufferbloat lives in our setup. Don't reach for it as a "performance fix" (patch 11 sndbuf-env-knob was shipped then dropped because the knob was never useful).
- **Process-of-elimination methodology for diagnosing latency tails.** When a user reports "feels laggy" but synthetic benchmarks look fine: A/B test EACH layer you control (VPN queue, kernel buffer, etc.) using a continuous probe (TCP-RTT every 50ms for 3 min) during the real workload. If both A and B produce identical distributions, the lag is in a layer you DON'T control (WiFi MAC, OS protocol stack, application protocol). Stop adding code at that point.
- **Screen sharing latency floor is the wireless medium, not the VPN.** With Mac mini on Ethernet → WiFi router → laptop on WiFi, ~12-13% of TCP-RTT samples land in the 40-160ms band regardless of any VPN tuning. That tail is WiFi MAC contention + Apple's RFB protocol bunching. Fixable only by: wired ethernet on both ends, a different remote desktop protocol, or WiFi 6E low-latency profile. Not fixable in the VPN layer.
- **Verify "shipped" feature claims against code, not docs.** Docs drift when features are scoped but never built. Four separate doc files (features.md, roadmap.md, competitive-analysis.md, performance.md) claimed DPLPMTUD was "✅ Done (v0.7.3) — first mesh VPN with RFC 8899" — but `internal/pmtud/` never existed and zero probe code was ever written. Same for "packet coalescing" and "multi-reader UDP." Always `grep` the codebase for the implementation before putting a claim in a competitive comparison or feature list. If the package/function doesn't exist, the feature doesn't exist.
- **A null result on defensive code is not grounds to drop it if the test doesn't exercise the failure mode.** Patch 09 (control/data priority queue) measured "no improvement" under our 1-on-1 LAN benchmarks. But those benchmarks never stressed the scenario it defends against (control-lane starvation under bulk load with concurrent handshakes). Dropping code because "benchmarks showed no difference" is only valid if the benchmarks actually exercised the thing the code protects against. If they don't, the null result tells you nothing about the code's value — it tells you the test is incomplete.
- **Measure the existing system before building a replacement.** The QUIC-into-Nebula thread is the archetypal example: we built `internal/quictransport/session.go` (transparent reconnect with TLS resumption) because empirical testing proved bare `quic-go` migration can't survive real outages. Then a 30-minute baseline test against the *existing* Nebula transport (`spike/nebula-baseline-evidence/`) showed Nebula already survives a 30s `ifconfig en0 down/up` with TCP intact and ~3s recovery. The replacement we'd built solved a problem that didn't exist at user scale. Always run the cheapest possible empirical test against the current system before committing days to a replacement. Particularly suspicious sources of false negatives: priors from older discovery-log entries (mine said "tunnels go dead in ~15s" — true for *idle* tunnels, false for *active* ones with continuous traffic, which is what real users have).
- **Verify "unique to us" / "first in class" competitive claims against actual competitor product state, not intuition.** Same failure mode as "verify shipped feature claims against code" but at the market-landscape layer. Three times in a single strategy session I labelled things as novel differentiators that weren't: multipath bonding ([Speedify](https://speedify.com/) has shipped WiFi+cellular bonding since 2014 across all platforms; ZeroTier has protocol-level multipath), user-defined DNS domains (NetBird shipped Custom DNS Zones in v0.63, January 2026), and "cross-platform vectorized crypto pipeline" (wireguard-go's per-core pool is already cross-platform; only the batch optimization that depends on UDP GSO/GRO is Linux-gated). Before labelling anything "first in class" or "novel," search competitor product pages, GitHub release notes, and recent comparison reviews. If someone else has shipped it, the right framing is "catching up" or "matching parity," not "unique differentiator." Competitive claims printed in strategy docs propagate into pitch decks; wrong ones are embarrassing when a prospect points them out.
- **"Ballpark" numbers from a single benchmark do not generalize to "the gap is X Mbps."** I repeatedly cited "900 Mbps gap between Nebula and Tailscale on Linux" as if it were a fixed quantity. That figure comes from [Defined Networking's blog post](https://www.defined.net/blog/nebula-is-not-the-fastest-mesh-vpn/) on specific c6i-class AWS hardware. Tailscale's own benchmark writeups ([10 Gb/s](https://tailscale.com/blog/more-throughput)) show multi-Gbps gains from their optimizations on faster hardware (5.4→7.3 Gbps on c6i.8xlarge, up to 13 Gbps on i5-12400). A performance gap measured on one hardware class rarely holds on faster hardware — Amdahl's law shifts the bottleneck. When citing a competitor gap, name the hardware, don't generalize.

### QUIC Connection Migration (quic-go)

Verified end-to-end with `hop-agent migration` against a deployed QUIC echo server (`internal/quictransport/`), with three vantage points: client probe log, client qlog, server-side tcpdump + packet logger. Evidence preserved at `spike/migration-evidence/`.

- **`quic.Connection.AddPath()` / `Path.Probe()` is a race-condition fix, not an outage fix.** Verified on real cellular (Yettel BG) and via `ifconfig en0 down` on macOS: when the underlying socket fails (ENETUNREACH), quic-go's send loop accumulates errors silently for ~50 seconds, then closes the connection with `connection_closed initiator=local` and ZERO error code. After that, `AddPath` still returns a path object but `Probe()` never emits a PATH_CHALLENGE frame on the wire — it just blocks until the caller's context times out. Even with the network fully restored on a different interface, migration cannot recover.
- **Pre-flight test packets are not enough to keep migration alive.** Sending 10× 1-byte UDP packets through a fresh socket establishes the route + CGNAT mapping (we verified this — server received them) but doesn't matter if the parent QUIC connection is already closed.
- **Network-state polling at 2s is too slow.** `localAddrFingerprint` only changed ~50s after `en0` went down on macOS (the kernel didn't surface the interface drop into `net.Interfaces()` immediately). By the time the change was visible, the QUIC connection was already in the closing state. To beat quic-go's silent close, network-change detection has to be sub-second (kqueue route monitor on Darwin, netlink on Linux).
- **Architecture for real-world reliability needs three layers, not just migration:**
  1. **Connection migration** — handles WiFi → WiFi handoff in <30s while connection is alive.
  2. **Transparent reconnect** — when quic-go closes, reopen a new QUIC connection with the same identity (TLS session resumption + 0-RTT), buffer outgoing during the reconnect window, replay on completion.
  3. **Multipath / parallel paths** — when bandwidth or reliability requires it, IETF MPQUIC (still draft, quic-go has experimental support).
  Layer 1 alone is what we built and tested; layers 2-3 are still pending. Real apps using QUIC for long-lived sessions (Cloudflare WARP, Apple Private Relay) implement all three.
- **Datagrams ARE NOT acknowledged by quic-go's loss detection.** This is RFC 9221 unreliable-datagram semantics. The `LOST seq=N` log lines in our probe are app-level timeouts (no echo received in 10s) — they don't tell us whether the underlying datagram was sent on the wire or not. The qlog `packet_sent` event is the only reliable indication of "left this host."
- **`docker logs` defaults to InfoLevel for quic-go internal logger.** When wrapping `net.PacketConn` for visibility (e.g., logging new src/dst addrs), the wrapper's logrus must default to InfoLevel — `nil` log defaults to WarnLevel and silently drops Info messages, which silently broke our packet logger on the first deploy.
- **Don't trust Docker tag pulls in dev-deploy unless the tag changes.** With `image_pull_policy = "missing"` (the Nomad default), Nomad re-pulls only when the cached tag is absent. If you push a new image with the same tag, Nomad keeps the cached old image. Bumping the commit hash (so the tag becomes `dev-NEWHASH`) forces a real pull. Check via `docker inspect <container> | jq '.[0].Created'` — should be after your push.

---

## Coding Principles

These rules are derived from 4 rounds of security review. Follow them for all new code.

### Secrets & Credentials

- **Never store secrets in plaintext.** Encrypt at rest (AES-256-GCM via `crypto.Encryptor`) or hash (SHA-256) depending on whether the value needs to be recovered.
  - Recoverable secrets (agent tokens, keys): encrypt with `Encryptor`
  - Compare-only secrets (session tokens, enrollment tokens, device codes): store SHA-256 hash
- **Tokens must be single-use and time-bounded.** Enrollment tokens get a 10-minute TTL. Device codes get 10 minutes. Bundle URLs get 15 minutes.
- **Consume tokens atomically.** Use a database transaction with `UPDATE ... WHERE token = ? AND status = 'pending'` + check `RowsAffected`. Never SELECT then UPDATE in separate operations — that's a TOCTOU race.
- **Use `crypto/subtle.ConstantTimeCompare`** for any token comparison in the agent. Never use `==` or `!=` for bearer tokens.
- **Never pass secrets as command-line arguments** when avoidable. Prefer `--token-stdin` (read from stdin) or `--token-file` (read from file). If `--token` flag is offered, document that it's visible in `ps`.

### API Handlers

- **Always call `http.MaxBytesReader`** on `r.Body` before decoding JSON. Default: `1 << 20` (1 MB). Uploads: explicit limit.
- **Always validate ownership** before operating on a resource. Every handler touching a network must check `network.UserID != user.ID`. Every handler touching a node must go through `requireNode()` which validates network→node chain.
- **Never serialize `*db.Node` or `*db.User` directly to JSON.** Always map to a response DTO (`NodeResponse`, `UserProfile`, etc.) to prevent leaking sensitive fields.
- **Use `writeJSON(w, v)` for 200 responses** and `writeJSONStatus(w, status, v)` for non-200 (e.g., 201 Created). Never call `w.WriteHeader()` then `writeJSON()` — the Content-Type header will be silently dropped.
- **Return `409 Conflict` on UNIQUE constraint violations**, not 500. Check with `db.IsUniqueViolation(err)`.
- **Audit security-significant actions.** Login, registration, shell connect, exec, port-forward start, node delete. Use `h.audit(userID, action, &networkID, &nodeID, details)`.
- **Rate-limit all public endpoints.** Use `auth.NewRateLimiter()` middleware. Pass `trustedProxy` to control `X-Forwarded-For` trust.
- **Apply `writeTimeout` to all non-streaming routes.** Shell and exec are streaming (no timeout). Everything else gets `http.TimeoutHandler(30s)`.

### Database

- **All migrations run in transactions.** `tx.Begin()` → execute SQL → record in `schema_migrations` → `tx.Commit()`. Rollback on any error.
- **Check `rows.Err()`** after every `for rows.Next()` loop.
- **Use `wdb` (write DB) for any operation that needs atomicity** — even reads that must be serialized with a subsequent write. The `rdb` (read DB) can return stale data.
- **Allocation queries (subnet, node IP) use MAX, not COUNT.** COUNT breaks when rows are deleted. MAX ensures monotonically increasing IDs. The UNIQUE constraint is the safety net.
- **Hash before storing, hash before querying.** If a token is hashed at rest (enrollment tokens, session tokens, device codes), the lookup query must hash the input before the WHERE clause.

### Agent

- **Never interpolate user input into shell commands.** Use `exec.Command("binary", "arg1", "arg2")` directly. Never use `fmt.Sprintf("cmd %s", input)` with `sh -c`.
- **Restrict file write paths.** Uploads go to `/var/hop-agent/uploads/` only. Validate with `filepath.Clean` + `strings.HasPrefix`.
- **Set `ReadHeaderTimeout`** on all HTTP servers (10s). Prevents Slowloris attacks.
- **Add a timeout on `cmd.Wait()`** in shell cleanup (5s). Prevents blocking on zombie processes.

### Networking & TLS

- **Session cookies must set `Secure` when behind HTTPS.** Use `r.TLS != nil || (TrustedProxy && X-Forwarded-Proto == "https")`.
- **Only trust `X-Forwarded-For` / `X-Forwarded-Proto` when `--trusted-proxy` is set.** These headers are trivially spoofable by direct clients.
- **Validate WebSocket Origin.** Default to same-origin check (`origin == "http(s)://" + host`). Allow explicit origins via `AllowedOrigins` config.
- **Never serve private key material over plain HTTP.** Bundle downloads must require HTTPS.
- **Set CORS headers** via middleware. Default: same-origin only. Configurable via `--allowed-origins`.

### Go Patterns

- **Panic on `crypto/rand.Read` failure.** If the system entropy source is broken, nothing is safe. All `rand.Read` calls must check the error.
- **Use `*int` / `*string` for optional DB fields** that can be NULL. Scan into pointer types.
- **Exported functions return errors; unexported helpers can panic** on truly impossible conditions (crypto/rand failure).
- **Port forward IDs and similar user-facing identifiers must be crypto-random**, not sequential. Sequential IDs are guessable.
- **Connection relays (io.Copy bidirectional) must use half-close.** After one direction's `io.Copy` returns, call `CloseWrite()` on the other connection so the reverse copy gets EOF.
- **Context values must never carry sensitive data.** Use `UserProfile` (no password hash) in request context, not `User`.
- **SQLite read pool: 20 connections, 5 idle.** Write pool: 1 connection. WAL mode. This is the right balance for an embedded database.
