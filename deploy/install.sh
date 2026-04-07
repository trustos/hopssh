#!/usr/bin/env bash
# Deploy hopssh control plane on a fresh Ubuntu/Oracle Linux server.
#
# Two modes:
#   1. Binary install (default): Downloads pre-built binaries from GitHub Releases.
#      ssh ubuntu@<ip> 'bash -s' < deploy/install.sh
#
#   2. Source build (--source): Builds from source (requires Go + Node.js, takes ~5 min).
#      ssh ubuntu@<ip> 'bash -s -- --source' < deploy/install.sh
set -euo pipefail

MODE="binary"
if [ "${1:-}" = "--source" ]; then
  MODE="source"
fi

echo "==> Installing hopssh control plane (${MODE} mode)..."

if [ "$MODE" = "binary" ]; then
  # Download pre-built binaries using the install script from GitHub.
  curl -fsSL https://raw.githubusercontent.com/trustos/hopssh/main/scripts/install.sh | bash -s -- --all

else
  # Build from source.
  ARCH=$(uname -m)
  case "$ARCH" in
    aarch64|arm64) GOARCH=arm64 ;;
    x86_64|amd64) GOARCH=amd64 ;;
    *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
  esac

  GOVERSION=1.24.2
  echo "==> Installing Go $GOVERSION ($GOARCH)..."
  curl -fsSL "https://go.dev/dl/go${GOVERSION}.linux-${GOARCH}.tar.gz" | sudo tar -C /usr/local -xzf -
  export PATH=/usr/local/go/bin:$PATH
  sudo ln -sf /usr/local/go/bin/go /usr/local/bin/go
  sudo ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt

  echo "==> Installing build dependencies..."
  if command -v apt-get &>/dev/null; then
    sudo apt-get update -y && sudo apt-get install -y git make patch curl ca-certificates
    curl -fsSL https://deb.nodesource.com/setup_22.x | sudo -E bash -
    sudo apt-get install -y nodejs
  elif command -v dnf &>/dev/null; then
    sudo dnf install -y git make patch curl
    curl -fsSL https://rpm.nodesource.com/setup_22.x | sudo bash -
    sudo dnf install -y nodejs
  fi

  echo "==> Cloning and building hopssh..."
  cd /opt
  sudo git clone https://github.com/trustos/hopssh.git
  cd hopssh
  sudo make setup
  sudo make build-all

  sudo cp hop-server /usr/local/bin/
  sudo cp hop-agent /usr/local/bin/
fi

# Detect public IP and install the service.
PUBLIC_IP=$(curl -s http://169.254.169.254/opc/v1/vnics/ 2>/dev/null | python3 -c "import sys,json; print(json.load(sys.stdin)[0]['publicIp'])" 2>/dev/null || curl -s ifconfig.me)
ENDPOINT="http://${PUBLIC_IP}:9473"

echo "==> Detected public IP: $PUBLIC_IP"
sudo hop-server install --endpoint "$ENDPOINT"

echo ""
echo "==> hopssh control plane installed!"
echo "    Dashboard: $ENDPOINT"
echo "    Data:      /var/lib/hopssh"
echo "    Logs:      journalctl -u hopssh -f"
echo ""
echo "    To enroll a server:"
echo "    1. Open $ENDPOINT in your browser"
echo "    2. Register an account"
echo "    3. Create a network"
echo "    4. Follow the Join instructions"
