#!/usr/bin/env bash
# Install hopssh control plane on a fresh Ubuntu/Oracle Linux server.
# Run after deploying the OCI instance:
#   ssh ubuntu@<public-ip> 'bash -s' < deploy/install.sh
set -euo pipefail

echo "==> Installing hopssh control plane..."

# Detect architecture
ARCH=$(uname -m)
case "$ARCH" in
  aarch64|arm64) GOARCH=arm64 ;;
  x86_64|amd64) GOARCH=amd64 ;;
  *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

# Install Go
GOVERSION=1.24.2
echo "==> Installing Go $GOVERSION ($GOARCH)..."
curl -fsSL "https://go.dev/dl/go${GOVERSION}.linux-${GOARCH}.tar.gz" | sudo tar -C /usr/local -xzf -
export PATH=$PATH:/usr/local/go/bin
export GOPATH=$HOME/go
export PATH=$PATH:$GOPATH/bin

# Install build deps
echo "==> Installing build dependencies..."
if command -v apt-get &>/dev/null; then
  sudo apt-get update -y && sudo apt-get install -y git make patch nodejs npm
elif command -v dnf &>/dev/null; then
  sudo dnf install -y git make patch nodejs npm
fi

# Clone and build
echo "==> Cloning and building hopssh..."
cd /opt
sudo git clone https://github.com/trustos/hopssh.git
cd hopssh
sudo make setup
sudo make build-all

# Install binaries
sudo cp hop-server /usr/local/bin/
sudo cp hop-agent /usr/local/bin/

# Create data directory
sudo mkdir -p /var/lib/hopssh
sudo chmod 700 /var/lib/hopssh

# Detect public IP
PUBLIC_IP=$(curl -s http://169.254.169.254/opc/v1/vnics/ 2>/dev/null | python3 -c "import sys,json; print(json.load(sys.stdin)[0]['publicIp'])" 2>/dev/null || curl -s ifconfig.me)
ENDPOINT="http://${PUBLIC_IP}:9473"

echo "==> Detected public IP: $PUBLIC_IP"
echo "==> Endpoint: $ENDPOINT"

# Create systemd service
sudo tee /etc/systemd/system/hopssh.service > /dev/null <<UNIT
[Unit]
Description=hopssh control plane
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/hop-server --data /var/lib/hopssh --endpoint $ENDPOINT
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT

sudo systemctl daemon-reload
sudo systemctl enable hopssh
sudo systemctl start hopssh

echo ""
echo "==> hopssh control plane installed!"
echo "    Dashboard: $ENDPOINT"
echo "    Data: /var/lib/hopssh"
echo "    Logs: journalctl -u hopssh -f"
echo ""
echo "    To enroll a server:"
echo "    1. Open $ENDPOINT in your browser"
echo "    2. Register an account"
echo "    3. Create a network"
echo "    4. Click 'Add Node' and copy the install command"
