# hopssh — Enrollment Guide

Four ways to add devices to your mesh network. All produce identical nodes
with the same capabilities — there's no server/client distinction.

---

## 1. Device Flow (Interactive — Recommended)

The safest option. No token ever touches the device.

```bash
# On any device:
curl -fsSL http://your-control-plane:9473/install.sh | sh
hop-agent enroll --endpoint http://your-control-plane:9473

  To enroll this node, open:  http://your-control-plane:9473/device
  Enter code:  HOP-K9M2
  Waiting for authorization...
```

Then in your browser (already logged into the dashboard):
1. Visit the device auth page
2. Enter the code `HOP-K9M2`
3. Select the network
4. Click **Authorize**

Back on the device:
```
  ✓ Enrolled (10.42.1.3/24)
  ✓ Nebula config written (lighthouse: 10.42.1.1 via your-control-plane:42001)
  ==> hop-agent service installed and started.
```

**Security**: The 6-character code is meaningless without your authenticated browser session. Nothing is stored in shell history or visible in `ps`.

**Best for**: Any device — servers, laptops, NAS, Raspberry Pi.

---

## 2. Token via stdin (Scriptable — No ps Leak)

Fast, scriptable, and the token never appears in process arguments.

```bash
# In the dashboard: click "Add Node" → copies a token
# Token expires in 10 minutes, single-use.

curl -fsSL http://your-control-plane:9473/install.sh | sh
echo "hop_Xk9mQ2..." | hop-agent enroll --token-stdin --endpoint http://your-control-plane:9473

  ✓ Enrolled (10.42.1.4/24)
  ==> hop-agent service installed and started.
```

**Security**: Token delivered via stdin — not visible in `ps` or `/proc/*/cmdline`. Single-use, expires in 10 minutes. Even if it leaks from terminal scroll-back, it's already consumed or expired.

**Best for**: Onboarding multiple servers quickly, team lead setting up a fleet.

---

## 3. Token as Argument (Quick — Visible in ps)

The simplest one-liner, but the token is visible in process listings.

```bash
curl -fsSL http://your-control-plane:9473/install.sh | sh
hop-agent enroll --token hop_Xk9mQ2... --endpoint http://your-control-plane:9473
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
# Future: Terraform/Pulumi provider (not yet implemented)
resource "hopssh_enrollment_token" "web" {
  network_id = hopssh_network.prod.id
  ttl        = "10m"
}

resource "aws_instance" "web" {
  ami           = "ami-xxx"
  instance_type = "t3.micro"
  user_data     = <<-EOF
    #!/bin/bash
    curl -fsSL http://your-control-plane:9473/install.sh | sh
    echo "${hopssh_enrollment_token.web.token}" | hop-agent enroll --token-stdin --endpoint http://your-control-plane:9473
  EOF
}
```

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

## Re-enrollment

Already enrolled? Use `--force` to clean up and re-enroll:

```bash
hop-agent enroll --force --endpoint http://your-control-plane:9473
```

This stops the existing service, removes old config, and enrolls fresh.

---

## Enrollment Modes Summary

| Mode | Command | Best for | Token visible? | Offline? |
|------|---------|----------|---------------|---------|
| Device flow | `hop-agent enroll --endpoint <url>` | Any device (interactive) | No | No |
| Token stdin | `echo <tok> \| hop-agent enroll --token-stdin --endpoint <url>` | Scripted setup | Briefly (stdin) | No |
| Token arg | `hop-agent enroll --token <tok> --endpoint <url>` | Quick demos | Yes (ps) | No |
| Bundle | `hop-agent enroll --bundle <path>` | Air-gapped | No | Yes |

All modes produce identical nodes. Capabilities (terminal, health, forward) are toggled per-node from the dashboard after enrollment — no distinction at enrollment time.

## Post-enrollment CLI

```bash
hop-agent status    # Check connection, cert expiry, service state
hop-agent info      # Node metadata, hostname, version
hop-agent help      # All available commands
```
