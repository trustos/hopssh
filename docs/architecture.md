# hopssh — Architecture

## System Overview

```
┌─────────────────────────────────────────────────────────────┐
│                    User's Browser                            │
│  Dashboard: networks, nodes, terminal, port forwards, audit  │
└──────────────────────┬──────────────────────────────────────┘
                       │ HTTPS
┌──────────────────────▼──────────────────────────────────────┐
│                  Control Plane                               │
│                                                              │
│  ┌─────────┐  ┌──────────┐  ┌─────────┐  ┌──────────────┐  │
│  │  Auth    │  │ Network  │  │  Node   │  │    Mesh      │  │
│  │ Session  │  │  Mgmt    │  │Registry │  │   Manager    │  │
│  │ API Key  │  │  PKI     │  │ Health  │  │  (Nebula)    │  │
│  │ OAuth    │  │  Enroll  │  │ Audit   │  │  On-demand   │  │
│  └─────────┘  └──────────┘  └─────────┘  └──────┬───────┘  │
│                                                   │          │
│  ┌─────────────────────────────────────────────┐  │          │
│  │  SQLite (users, networks, nodes, audit)     │  │          │
│  └─────────────────────────────────────────────┘  │          │
└───────────────────────────────────────────────────┼──────────┘
                                                    │
                              Nebula tunnel (encrypted, UDP)
                              Outbound from agent, NAT-friendly
                                                    │
┌───────────────────────────────────────────────────▼──────────┐
│                Customer's Server                              │
│                                                               │
│  ┌──────────────────────────────────────────────────────┐    │
│  │  hop-agent                                            │    │
│  │                                                       │    │
│  │  ├─ Nebula instance (outbound UDP tunnel)             │    │
│  │  ├─ /health     → hostname, OS, arch, uptime          │    │
│  │  ├─ /shell      → WebSocket PTY (bash)                │    │
│  │  └─ TCP relay   → port forwarding to localhost        │    │
│  │                                                       │    │
│  │  Auth: per-node Bearer token                          │    │
│  │  Ports: none inbound (outbound UDP only)              │    │
│  └──────────────────────────────────────────────────────┘    │
└──────────────────────────────────────────────────────────────┘
```

## Data Model

```
users
  ├─ id, email, name, password_hash, created_at
  └─ auth: GitHub OAuth or email/password

api_keys
  ├─ id, user_id, name, key_hash, last_used_at, created_at
  └─ for CLI and Terraform provider auth

networks
  ├─ id, user_id, name, slug
  ├─ nebula_ca_cert, nebula_ca_key (encrypted)
  ├─ nebula_subnet (e.g., "10.42.1.0/24")
  └─ created_at

nodes
  ├─ id, network_id, hostname, os, arch
  ├─ nebula_cert, nebula_key (encrypted)
  ├─ nebula_ip (e.g., "10.42.1.2/24")
  ├─ enrollment_token_hash
  ├─ agent_real_ip (discovered on first connect)
  ├─ status (online/offline)
  ├─ last_seen_at
  └─ created_at

audit_log
  ├─ id, user_id, node_id, network_id
  ├─ action (connect, disconnect, port_forward)
  ├─ details_json
  └─ created_at

team_members  (Team tier)
  ├─ network_id, user_id, role (admin/member/viewer)
  └─ invited_by, created_at
```

## Enrollment Flow

### New server (one-liner)

```
User clicks "Add Node" in dashboard
  → generates one-time enrollment token
  → shows: curl -fsSL https://hopssh.com/install | bash -s -- --token <token>

User pastes on server:
  1. Script downloads hop-agent + nebula binaries
  2. Agent calls POST /api/enroll with enrollment token
  3. Control plane returns: CA cert, node cert, node key, agent token, server addr
  4. Agent writes certs to /etc/hop-agent/
  5. Agent starts Nebula (outbound UDP to control plane)
  6. Agent registers with control plane via mesh
  7. Node appears in dashboard as "online"
```

### Existing infrastructure (Terraform)

```hcl
resource "hopssh_network" "prod" {
  name = "production"
}

resource "aws_instance" "app" {
  user_data = hopssh_network.prod.bootstrap_script
}
```

### Existing infrastructure (CLI batch)

```bash
hop enroll --hosts inventory.txt --ssh-key ~/.ssh/id_ed25519
```

CLI SSHes in once, installs agent, SSH never needed again.

## Security Model

### What the control plane knows
- Network topology (which nodes exist, their IPs)
- User sessions (who's logged in)
- Audit trail (who connected to what, when)
- Encrypted PKI material (CA keys, node keys — encrypted at rest)

### What the control plane NEVER has
- SSH keys (users don't have SSH keys — they use the browser)
- Server passwords or root credentials
- Cloud provider credentials (AWS/GCP/OCI keys)
- File contents or command output (terminal traffic is relayed, not stored)

### Encryption
- **In transit:** Nebula (Noise Protocol, Curve25519) — end-to-end encrypted
- **At rest:** AES-256-GCM for all PKI material in SQLite
- **Per-network isolation:** Each network has its own CA. Compromising one network's CA doesn't affect others.

### Agent security
- Runs as systemd service, restarts on failure
- Authenticates with per-node Bearer token
- Listens only on Nebula overlay interface (not public network)
- No inbound ports on public interface
- If agent stops, control plane loses all access to that server

## Mesh Networking

### Why Nebula (not WireGuard)
- **Userspace mode** — no kernel module, no root for tunnel creation on control plane
- **Built-in PKI** — native certificate model, no external CA needed
- **NAT traversal** — UDP hole punching, works behind most NATs
- **Per-network isolation** — separate CA per network = cryptographic isolation
- **MIT licensed** — no restrictions on commercial use

### Tunnel lifecycle
- **On-demand:** Tunnel created when user opens terminal or port forward
- **Idle reaper:** Tunnels closed after 5 minutes of inactivity
- **Per-node:** Each node gets its own Nebula instance on the control plane
- **Retry logic:** Handles agent session expiration after control plane restart

## Scalability Path

| Scale | Architecture | Nodes |
|---|---|---|
| MVP | Single binary, embedded SQLite | Up to 500 nodes |
| Growth | Read replicas, batched writes | Up to 2,000 nodes |
| Scale | PostgreSQL, distributed control plane | Unlimited |

The single-binary SQLite architecture handles the first 500+ paying nodes comfortably.
Migration to PostgreSQL only needed if product succeeds significantly.
