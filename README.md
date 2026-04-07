<p align="center">
  <img src="frontend/static/logo.svg" alt="hopssh" width="200" />
</p>

<h3 align="center">Hop into your network. Your servers, your rules.</h3>

<p align="center">
  Encrypted mesh networking with P2P, relay fallback, built-in DNS, web terminal, and a management dashboard.<br/>
  The best self-hosted alternative to Tailscale and ZeroTier. Single binary, zero infrastructure.
</p>

<p align="center">
  <a href="https://hopssh.com">Website</a> &middot;
  <a href="docs/architecture.md">Architecture</a> &middot;
  <a href="docs/enrollment.md">Enrollment Guide</a> &middot;
  <a href="docs/development.md">Development</a>
</p>

---

## What is hopssh?

hopssh creates encrypted mesh networks between your devices. Connect your servers, laptops, and phones with P2P tunnels — access services by name (`jellyfin.home`, `nas.prod`), manage everything from a browser dashboard, and SSH into any node from the web terminal.

- **Single binary** — control plane, lighthouse, relay, DNS, web UI, all in one
- **Web terminal** — SSH into any node from your browser (no one else has this)
- **User-defined DNS** — `jellyfin.zero`, `nas.home`, `db.prod` — pick your own domain per network
- **Per-node capabilities** — toggle terminal, health check, port forwarding per device from the dashboard
- **Teams & invites** — share networks with invite links, admin/member roles
- **Self-hosted** — your infrastructure, your keys, no external service

## How it compares

| | ZeroTier | Tailscale | hopssh |
|---|---|---|---|
| P2P mesh | Yes | Yes | Yes (Nebula) |
| Relay fallback | Roots (UDP) | DERP (TCP) | Lighthouse relay (UDP) |
| Self-hosted control | Clunky | Headscale (separate) | **First-class (single binary)** |
| Web terminal | No | No | **Yes** |
| DNS | Manual | MagicDNS (.ts.net) | **User-defined domains** |
| Per-node capabilities | No | Via ACLs | **Dashboard toggles** |
| Teams & invites | No | Via admin console | **Invite links with expiry** |
| Management UI | Hosted only | Limited | **Always (embedded in binary)** |
| Protocol | Custom | WireGuard | Nebula (Noise, Curve25519) |

## Quickstart

### 1. Install the control plane

```bash
# Download (Linux/macOS)
curl -fsSL https://github.com/trustos/hopssh/releases/latest/download/install.sh | sh -s -- --server

# Install as a service
sudo hop-server install --endpoint http://YOUR_PUBLIC_IP:9473

# Open the dashboard
open http://YOUR_PUBLIC_IP:9473
```

Or with Docker:

```bash
docker run -d --name hopssh \
  -p 9473:9473 -p 42001-42100:42001-42100/udp \
  -v hopssh-data:/data \
  -e HOPSSH_ENDPOINT=http://YOUR_PUBLIC_IP:9473 \
  ghcr.io/trustos/hopssh:latest
```

### 2. Create a network

Open the dashboard, register an account, and create a network. Choose a DNS domain (e.g., `.home`, `.prod`, `.lab`).

### 3. Add nodes

On any device (server, laptop, NAS, Raspberry Pi):

```bash
# Install hop-agent
curl -fsSL http://YOUR_CONTROL_PLANE:9473/install.sh | sh

# Enroll (interactive device flow)
hop-agent enroll --endpoint http://YOUR_CONTROL_PLANE:9473
```

That's it. The node appears in the dashboard. Access it by name: `hostname.yourdomain`.

## CLI Reference

### hop-server (control plane)

```
hop-server                              Start the control plane
hop-server install --endpoint <url>     Install as a systemd/launchd service
hop-server uninstall [--purge]          Remove the service
hop-server update                       Self-update from GitHub Releases
hop-server version                      Print version
hop-server healthz                      Health check (for containers)
```

**Environment variables:** `HOPSSH_ENDPOINT`, `HOPSSH_ADDR`, `HOPSSH_DATA`, `HOPSSH_TRUSTED_PROXY`, `HOPSSH_ALLOWED_ORIGINS`, `HOPSSH_ENCRYPTION_KEY`

### hop-agent (node agent)

```
hop-agent                               Start the agent (default)
hop-agent enroll --endpoint <url>       Join a mesh network
hop-agent enroll --force                Re-enroll (clean + fresh)
hop-agent status                        Show connection status
hop-agent info                          Show node information
hop-agent help                          Show all commands
hop-agent install                       Install as a service
hop-agent uninstall [--purge]           Remove service + config
hop-agent update                        Self-update
hop-agent version                       Print version
```

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│               hopssh Control Plane (single binary)           │
│                                                              │
│  API + Web UI (:9473)  │  Per-Network Nebula Instances       │
│                        │  Lighthouse + Relay + DNS            │
│  SQLite  │  PKI        │  (one per network, isolated CAs)    │
└──────────────────┬────────────────────┬──────────────────────┘
                   │ TCP :9473          │ UDP :42001-N
                   │                    │
          ┌────────┘              ┌─────┘
          │                       │
     ┌────┴────┐           ┌─────┴────────────────┐
     │ Browser │           │ Nodes                  │
     │ (manage,│           │                        │
     │ terminal│           │  Node A ←─P2P─→ Node B │
     │  proxy) │           │     └──relay──┘        │
     └─────────┘           │  Node C (laptop)       │
                           └────────────────────────┘
```

## Security

- **E2E encrypted** — Nebula (Noise Protocol, Curve25519, ChaCha20-Poly1305)
- **Per-network CA** — each network has its own certificate authority. One breach doesn't affect others.
- **Short-lived certificates** — 24h auto-renewal with jitter
- **Relay is blind** — cannot decrypt traffic (forwards opaque Nebula packets)
- **Secrets encrypted at rest** — CA keys, node keys use AES-256-GCM
- **No stored credentials** — control plane never holds SSH keys, cloud creds, or passwords
- **Self-hosted** — you control the CA, the lighthouse, the relay, everything

## Docker Compose

```bash
git clone https://github.com/trustos/hopssh.git
cd hopssh
docker compose up --build
# Open http://localhost:9473
```

## Development

```bash
make setup          # Vendor deps + apply Nebula patch
make build-all      # Build frontend + Go binaries
make dev            # Run backend + frontend with hot reload
make release        # Tag + push (triggers GitHub Actions build)
```

See [docs/development.md](docs/development.md) for the full developer guide.

## License

hopssh builds on [Nebula](https://github.com/slackhq/nebula) (MIT).
