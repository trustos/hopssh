#!/usr/bin/env bash
# hopssh install script — downloads pre-built binaries from GitHub Releases.
# This script is for first-time installs when no control plane exists yet.
# Once a control plane is running, use: curl -fsSL http://your-server:9473/install.sh | sh
#
# Usage:
#   curl -fsSL https://get.hopssh.com | sh                    # install hop-agent (default)
#   curl -fsSL https://get.hopssh.com | sh -s -- --server     # install hop-server
#   curl -fsSL https://get.hopssh.com | sh -s -- --all        # install both
#   curl -fsSL https://get.hopssh.com | sh -s -- --version v0.2.0  # specific version
set -euo pipefail

GITHUB_REPO="trustos/hopssh"
COMPONENT="agent"
VERSION=""

while [ $# -gt 0 ]; do
  case "$1" in
    --server)  COMPONENT="server"; shift ;;
    --all)     COMPONENT="all"; shift ;;
    --version) VERSION="$2"; shift 2 ;;
    *)         echo "Unknown option: $1"; exit 1 ;;
  esac
done

# Detect OS
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  linux)  OS="linux" ;;
  darwin) OS="darwin" ;;
  *)      echo "Error: Unsupported operating system: $OS"; echo "hopssh supports Linux and macOS."; exit 1 ;;
esac

# Detect architecture
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64)   ARCH="amd64" ;;
  aarch64|arm64)   ARCH="arm64" ;;
  *)               echo "Error: Unsupported architecture: $ARCH"; echo "hopssh supports x86_64 and ARM64."; exit 1 ;;
esac

# Determine version
if [ -z "$VERSION" ]; then
  VERSION=$(curl -fsSL "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" 2>/dev/null | grep -o '"tag_name":"[^"]*"' | cut -d'"' -f4) || true
  if [ -z "$VERSION" ]; then
    echo "Error: Could not determine latest version from GitHub."
    echo "If rate limited, set GITHUB_TOKEN or specify: --version v0.1.0"
    exit 1
  fi
fi

echo "==> Installing hopssh ${VERSION} (${OS}/${ARCH})"

DOWNLOAD_BASE="https://github.com/${GITHUB_REPO}/releases/download/${VERSION}"
INSTALL_DIR="/usr/local/bin"
SUDO=""
if [ "$(id -u)" -ne 0 ]; then
  if command -v sudo &>/dev/null; then
    SUDO="sudo"
  else
    echo "Error: Not running as root and sudo is not available."
    echo "Run as root or install sudo."
    exit 1
  fi
fi

# Download checksums once
CHECKSUMS=$(curl -fsSL "${DOWNLOAD_BASE}/SHA256SUMS" 2>/dev/null) || true

install_binary() {
  local name="$1"
  local bin="hop-${name}-${OS}-${ARCH}"
  local url="${DOWNLOAD_BASE}/${bin}"
  local tmpfile
  tmpfile=$(mktemp)

  echo "==> Downloading ${bin}..."
  if ! curl -fsSL "${url}" -o "${tmpfile}"; then
    rm -f "${tmpfile}"
    echo "Error: Failed to download ${bin} from ${url}"
    echo "Check that version ${VERSION} exists at:"
    echo "  https://github.com/${GITHUB_REPO}/releases/tag/${VERSION}"
    exit 1
  fi

  # Verify checksum
  if [ -n "$CHECKSUMS" ]; then
    local expected
    expected=$(echo "$CHECKSUMS" | grep "${bin}" | awk '{print $1}')
    if [ -n "$expected" ]; then
      local actual
      if command -v sha256sum &>/dev/null; then
        actual=$(sha256sum "${tmpfile}" | awk '{print $1}')
      elif command -v shasum &>/dev/null; then
        actual=$(shasum -a 256 "${tmpfile}" | awk '{print $1}')
      fi
      if [ -n "$actual" ] && [ "$actual" != "$expected" ]; then
        rm -f "${tmpfile}"
        echo "Error: Checksum verification failed for ${bin}."
        echo "  Expected: ${expected}"
        echo "  Got:      ${actual}"
        echo "The download may be corrupted. Try again."
        exit 1
      fi
      echo "    Checksum verified."
    fi
  fi

  $SUDO install -m 755 "${tmpfile}" "${INSTALL_DIR}/hop-${name}"
  rm -f "${tmpfile}"
  echo "    Installed: ${INSTALL_DIR}/hop-${name}"
}

case "$COMPONENT" in
  agent)
    install_binary "agent"
    echo ""
    echo "==> hop-agent installed!"
    if [ -f /etc/hop-agent/node.crt ] || [ -f "${HOME}/Library/Application Support/hopssh/node.crt" ] || [ -f "${HOME}/.config/hopssh/node.crt" ]; then
      echo ""
      echo "    Existing enrollment found. Restart the service to use the new version:"
      if command -v launchctl &>/dev/null; then
        echo "      sudo launchctl unload /Library/LaunchDaemons/com.hopssh.agent.plist"
        echo "      sudo launchctl load /Library/LaunchDaemons/com.hopssh.agent.plist"
      else
        echo "      sudo systemctl restart hop-agent"
      fi
    else
      echo ""
      echo "    Next: enroll this device into your network:"
      echo "      sudo hop-agent enroll --endpoint https://your-control-plane:9473"
      echo ""
      echo "    Or with a token from the dashboard:"
      echo "      echo '<token>' | sudo hop-agent enroll --token-stdin --endpoint https://your-control-plane:9473"
    fi
    ;;
  server)
    install_binary "server"
    echo ""
    echo "==> hop-server installed!"
    echo "    Next steps:"
    echo "      sudo hop-server install --endpoint http://YOUR_PUBLIC_IP:9473"
    ;;
  all)
    install_binary "agent"
    install_binary "server"
    echo ""
    echo "==> hop-agent and hop-server installed!"
    ;;
esac
