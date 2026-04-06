#!/usr/bin/env bash
# hopssh agent installer
# Usage: curl -fsSL https://hopssh.com/install | sudo bash -s -- --token <enrollment-token>
set -euo pipefail

ENROLLMENT_TOKEN=""
ENDPOINT="https://hopssh.com"
NEBULA_VERSION="1.10.3"
AGENT_DIR="/etc/hop-agent"

# Parse arguments.
while [[ $# -gt 0 ]]; do
  case "$1" in
    --token) ENROLLMENT_TOKEN="$2"; shift 2 ;;
    --endpoint) ENDPOINT="$2"; shift 2 ;;
    --nebula-version) NEBULA_VERSION="$2"; shift 2 ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

if [[ -z "$ENROLLMENT_TOKEN" ]]; then
  echo "Error: --token is required"
  echo "Usage: curl -fsSL https://hopssh.com/install | sudo bash -s -- --token <token>"
  exit 1
fi

# Detect OS and architecture.
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

if [[ "$OS" != "linux" ]]; then
  echo "Unsupported OS: $OS (only Linux is supported)"
  exit 1
fi

echo "==> hopssh agent installer"
echo "    OS: $OS, Arch: $ARCH"
echo "    Endpoint: $ENDPOINT"

# Create directories.
mkdir -p "$AGENT_DIR"
mkdir -p /usr/local/bin

# Download Nebula.
echo "==> Downloading Nebula v${NEBULA_VERSION}..."
NEBULA_URL="https://github.com/slackhq/nebula/releases/download/v${NEBULA_VERSION}/nebula-linux-${ARCH}.tar.gz"
curl -fsSL "$NEBULA_URL" | tar -xz -C /usr/local/bin nebula nebula-cert
chmod +x /usr/local/bin/nebula /usr/local/bin/nebula-cert

# Download hop-agent.
echo "==> Downloading hop-agent..."
AGENT_URL="${ENDPOINT}/api/agent/binary/${OS}/${ARCH}"
curl -fsSL -o /usr/local/bin/hop-agent "$AGENT_URL"
chmod +x /usr/local/bin/hop-agent

# Enroll with the control plane.
echo "==> Enrolling with control plane..."
HOSTNAME=$(hostname)
ENROLL_RESPONSE=$(curl -fsSL -X POST "${ENDPOINT}/api/enroll" \
  -H "Content-Type: application/json" \
  -d "{\"token\":\"${ENROLLMENT_TOKEN}\",\"hostname\":\"${HOSTNAME}\",\"os\":\"${OS}\",\"arch\":\"${ARCH}\"}")

# Extract enrollment data.
CA_CERT=$(echo "$ENROLL_RESPONSE" | python3 -c "import sys,json; print(json.load(sys.stdin)['caCert'])" 2>/dev/null || \
          echo "$ENROLL_RESPONSE" | jq -r '.caCert')
NODE_CERT=$(echo "$ENROLL_RESPONSE" | python3 -c "import sys,json; print(json.load(sys.stdin)['nodeCert'])" 2>/dev/null || \
            echo "$ENROLL_RESPONSE" | jq -r '.nodeCert')
NODE_KEY=$(echo "$ENROLL_RESPONSE" | python3 -c "import sys,json; print(json.load(sys.stdin)['nodeKey'])" 2>/dev/null || \
           echo "$ENROLL_RESPONSE" | jq -r '.nodeKey')
AGENT_TOKEN=$(echo "$ENROLL_RESPONSE" | python3 -c "import sys,json; print(json.load(sys.stdin)['agentToken'])" 2>/dev/null || \
              echo "$ENROLL_RESPONSE" | jq -r '.agentToken')
SERVER_IP=$(echo "$ENROLL_RESPONSE" | python3 -c "import sys,json; print(json.load(sys.stdin)['serverIP'])" 2>/dev/null || \
            echo "$ENROLL_RESPONSE" | jq -r '.serverIP')

if [[ -z "$CA_CERT" || "$CA_CERT" == "null" ]]; then
  echo "Error: Enrollment failed. Response:"
  echo "$ENROLL_RESPONSE"
  exit 1
fi

# Write certificates.
echo "$CA_CERT" > "$AGENT_DIR/ca.crt"
echo "$NODE_CERT" > "$AGENT_DIR/node.crt"
echo "$NODE_KEY" > "$AGENT_DIR/node.key"
chmod 600 "$AGENT_DIR/node.key"

# Write agent token.
echo "$AGENT_TOKEN" > "$AGENT_DIR/token"
chmod 600 "$AGENT_DIR/token"

# Detect server's public IP for static_host_map.
# The enrollment response gives us the server's Nebula IP; we need the real IP.
# Extract from the endpoint URL.
SERVER_HOST=$(echo "$ENDPOINT" | sed -E 's|https?://||' | sed 's|/.*||' | sed 's|:.*||')

# Write Nebula config.
mkdir -p /etc/nebula
cat > /etc/nebula/config.yaml <<NEBULA_EOF
pki:
  ca: ${AGENT_DIR}/ca.crt
  cert: ${AGENT_DIR}/node.crt
  key: ${AGENT_DIR}/node.key

static_host_map:
  "${SERVER_IP}": ["${SERVER_HOST}:41820"]

lighthouse:
  am_lighthouse: false
  hosts:
    - "${SERVER_IP}"

listen:
  host: 0.0.0.0
  port: 41820

punchy:
  punch: true
  respond: true

tun:
  dev: nebula1

firewall:
  outbound:
    - port: any
      proto: any
      host: any
  inbound:
    - port: any
      proto: tcp
      groups:
        - server
    - port: any
      proto: icmp
      host: any
NEBULA_EOF

# Create Nebula systemd service.
cat > /etc/systemd/system/nebula.service <<SERVICE_EOF
[Unit]
Description=Nebula mesh VPN
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/nebula -config /etc/nebula/config.yaml
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
SERVICE_EOF

# Create hop-agent systemd service.
cat > /etc/systemd/system/hop-agent.service <<SERVICE_EOF
[Unit]
Description=hopssh agent
After=nebula.service
Requires=nebula.service

[Service]
Type=simple
ExecStart=/usr/local/bin/hop-agent --listen :41820 --token-file ${AGENT_DIR}/token
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
SERVICE_EOF

# Enable and start services.
systemctl daemon-reload
systemctl enable nebula hop-agent
systemctl start nebula
sleep 2
systemctl start hop-agent

echo ""
echo "==> hopssh agent installed successfully!"
echo "    Nebula: running on nebula1 interface"
echo "    Agent:  listening on :41820"
echo "    Server: ${SERVER_IP} via ${SERVER_HOST}:41820"
echo ""
echo "    The node will appear in your dashboard momentarily."
