# hopssh — Enrollment Guide

Five ways to add devices to your mesh network.

---

## 1. Device Flow (Interactive — Recommended)

The safest option. No token ever touches the server.

```bash
# On the server:
curl -fsSL https://hopssh.com/install | sh
sudo hop-agent enroll

  To enroll this node, open:  https://hopssh.com/device
  Enter code:  HOP-K9M2
  Waiting for authorization...
```

Then in your browser (already logged into the dashboard):
1. Visit `hopssh.com/device`
2. Enter the code `HOP-K9M2`
3. Select the network (e.g., "production")
4. Click **Authorize**

Back on the server:
```
  ✓ Enrolled (10.42.1.3)
  ✓ Nebula config written (server: 10.42.1.1 via hopssh.com)
  ✓ Agent started
```

**Security**: The 6-character code is meaningless without your authenticated browser session. Nothing is stored in shell history or visible in `ps`.

**Best for**: Solo developers, ad-hoc VPS setup, one-off servers.

---

## 2. Token via stdin (Scriptable — No ps Leak)

Fast, scriptable, and the token never appears in process arguments.

```bash
# In the dashboard: click "Add Node" → copies a token
# Token expires in 10 minutes, single-use.

curl -fsSL https://hopssh.com/install | sh
echo "hop_Xk9mQ2..." | sudo hop-agent enroll --token-stdin

  ✓ Enrolled (10.42.1.4)
  ✓ Agent started
```

Or in one line:
```bash
curl -fsSL https://hopssh.com/install | sh && \
  echo "hop_Xk9mQ2..." | sudo hop-agent enroll --token-stdin
```

**Security**: Token delivered via stdin — not visible in `ps` or `/proc/*/cmdline`. Single-use, expires in 10 minutes. Even if it leaks from terminal scroll-back, it's already consumed or expired.

**Best for**: Onboarding multiple servers quickly, team lead setting up a fleet.

---

## 3. Token as Argument (Quick — Visible in ps)

The simplest one-liner, but the token is visible in process listings.

```bash
curl -fsSL https://hopssh.com/install | sh
sudo hop-agent enroll --token hop_Xk9mQ2...
```

**Security**: Token visible in `ps` and shell history while the command runs. Acceptable if you trust all users on the machine. Still single-use + 10-minute TTL.

**Best for**: Quick demos, trusted single-user machines, throwaway VMs.

---

## 4. Pre-bundled Tarball (Offline / Air-Gapped)

Everything pre-generated. No API calls during install.

```bash
# In the dashboard: "Add Node" → "Download Bundle"
# Downloads: hop-bundle-abc123.tar.gz (single-use URL, expires in 15 minutes)

# Transfer to server (SCP, USB, etc.):
scp hop-bundle-abc123.tar.gz server:/tmp/

# On the server:
sudo hop-agent enroll --bundle /tmp/hop-bundle-abc123.tar.gz

  ✓ Bundle extracted to /etc/hop-agent
  ✓ Agent started
```

Or manually:
```bash
sudo tar xzf /tmp/hop-bundle-abc123.tar.gz -C /
sudo systemctl enable --now nebula hop-agent
```

**Security**: No enrollment token at all — the bundle contains pre-signed certificates. The download URL is crypto-random, single-use, and expires in 15 minutes. After download, the URL is invalidated.

**Best for**: Air-gapped environments, restricted networks, compliance-heavy setups.

---

## 5. Terraform / Pulumi (Infrastructure-as-Code)

For teams managing servers programmatically.

```hcl
resource "hopssh_enrollment_token" "web" {
  network_id = hopssh_network.prod.id
  ttl        = "10m"
}

resource "aws_instance" "web" {
  ami           = "ami-xxx"
  instance_type = "t3.micro"
  user_data     = <<-EOF
    #!/bin/bash
    curl -fsSL https://hopssh.com/install | sh
    echo "${hopssh_enrollment_token.web.token}" | hop-agent enroll --token-stdin
  EOF
}
```

**Security**: Token generated at `terraform apply` time, injected via cloud-init user-data. Short-lived (10 min), consumed at first boot.

**Best for**: Cloud-native teams, CI/CD pipelines, auto-scaling groups.

---

## Token Lifecycle

```
Dashboard: "Add Node"
    │
    ├─ Token created (32-byte hex, SHA-256 hashed in DB)
    │  └─ Expires in 10 minutes
    │  └─ Single-use (consumed atomically on first enrollment)
    │
    ├─ Dashboard shows countdown: "Token expires in 9:42"
    │
    ├─ Agent sends token to /api/enroll
    │  └─ Server: hash(token) → lookup → verify not expired → consume → issue certs
    │
    └─ After enrollment:
       ├─ Token is NULL in DB (consumed)
       ├─ Node has short-lived Nebula certificate (24h, auto-renewed)
       ├─ Node has encrypted agent token for ongoing auth
       └─ Agent connects to lighthouse and joins the mesh
```

## Device Code Lifecycle

```
Agent: POST /api/device/code
    │
    ├─ Server creates: { device_code, user_code: "HOP-K9M2", expires: 10min }
    │
    ├─ Agent displays user_code, polls POST /api/device/poll every 5s
    │
    ├─ User visits /device in browser, enters "HOP-K9M2"
    │  └─ Browser: POST /api/device/authorize { userCode, networkId }
    │  └─ Server: marks device_code as "authorized"
    │
    ├─ Agent poll sees "authorized" → receives certs
    │  └─ Server: creates node, issues cert, marks device_code "completed"
    │
    └─ Agent installs certs, writes config, connects to lighthouse
```

---

## 6. Client Join (Laptops/Phones)

For accessing services from your personal devices (not servers).

```bash
# On your laptop:
hop client join --network <network-id> --endpoint https://hopssh.com

  Authenticating... (opens browser for login)
  ✓ Joined network "home" (10.42.1.5)
  ✓ DNS configured: .zero → mesh
  ✓ Connected to lighthouse

# Now access your services:
curl http://jellyfin.zero:8096
ssh nas.zero
ping immich.zero
```

**How it works:**
1. Authenticates with the control plane (browser OAuth or token)
2. Gets a Nebula certificate with group `user` (not `agent`)
3. Starts embedded Nebula in userspace (connects to lighthouse)
4. Configures split DNS so `.zero` (or whatever your network domain is) resolves through the mesh
5. P2P connections to agents when possible, relay fallback when not

**Security:**
- Client cert is `user` group — can only access ports the agent explicitly exposes
- Cannot access the agent management API (that's `admin` group only)
- Short-lived cert (24h), auto-renewed while the client is running
- Split DNS: only your mesh domain goes through the mesh. All other DNS is unchanged.

**Best for:** Accessing self-hosted services (Jellyfin, Immich, Paperless-ngx, Home Assistant) from anywhere.

---

## Enrollment Modes Summary

| Mode | Command | Who uses it | Token on device? | Offline? |
|------|---------|-------------|-----------------|---------|
| Device flow | `hop-agent enroll` | Server enrollment (interactive) | No | No |
| Token stdin | `echo <tok> \| hop-agent enroll --token-stdin` | Server enrollment (scripted) | Briefly | No |
| Token arg | `hop-agent enroll --token <tok>` | Quick demos | Yes (ps visible) | No |
| Bundle | `hop-agent enroll --bundle <path>` | Air-gapped servers | No | Yes |
| Client join | `hop client join --network <id>` | Laptops, phones | Via browser auth | No |
| Terraform | Via provider | IaC pipelines | In TF state | No |
