package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/trustos/hopssh/internal/buildinfo"
)

const (
	githubRepo       = "trustos/hopssh"
	githubReleasesURL = "https://api.github.com/repos/" + githubRepo + "/releases/latest"
	githubDownloadURL = "https://github.com/" + githubRepo + "/releases/download"
	versionCacheTTL  = 5 * time.Minute
)

// validBinaryName matches hop-agent-linux-amd64, hop-server-darwin-arm64, etc.
var validBinaryName = regexp.MustCompile(`^hop-(agent|server)-(linux|darwin|windows)-(amd64|arm64)(\.exe)?$`)

// DistributionHandler serves install scripts, binary downloads, and version info.
type DistributionHandler struct {
	Endpoint string // Public URL of this control plane

	mu            sync.RWMutex
	cachedVersion string
	cachedAt      time.Time
}

// LatestVersion returns the latest release version (cached, refreshed hourly).
func (h *DistributionHandler) LatestVersion() string {
	h.mu.RLock()
	if h.cachedVersion != "" && time.Since(h.cachedAt) < versionCacheTTL {
		v := h.cachedVersion
		h.mu.RUnlock()
		return v
	}
	h.mu.RUnlock()

	// Fetch from GitHub API.
	version := h.fetchLatestVersion()

	h.mu.Lock()
	h.cachedVersion = version
	h.cachedAt = time.Now()
	h.mu.Unlock()

	return version
}

func (h *DistributionHandler) fetchLatestVersion() string {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(githubReleasesURL)
	if err != nil {
		log.Printf("[dist] failed to fetch latest release: %v", err)
		return buildinfo.Version // fall back to own version
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		log.Printf("[dist] GitHub API returned %d: %s", resp.StatusCode, string(body))
		return buildinfo.Version
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		log.Printf("[dist] failed to parse release response: %v", err)
		return buildinfo.Version
	}

	if release.TagName != "" {
		return release.TagName
	}
	return buildinfo.Version
}

// Version returns the latest available version as JSON.
// GET /version — public, no auth.
func (h *DistributionHandler) Version(w http.ResponseWriter, r *http.Request) {
	latest := h.LatestVersion()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"version": latest,
		"current": buildinfo.Version,
	})
}

// Download redirects to the GitHub Release asset for the requested binary.
// GET /download/{binary} — public, no auth.
func (h *DistributionHandler) Download(w http.ResponseWriter, r *http.Request) {
	binary := chi.URLParam(r, "binary")

	if !validBinaryName.MatchString(binary) {
		http.Error(w, "Invalid binary name. Expected: hop-{agent|server}-{linux|darwin|windows}-{amd64|arm64}", http.StatusBadRequest)
		return
	}

	version := h.LatestVersion()
	url := fmt.Sprintf("%s/%s/%s", githubDownloadURL, version, binary)
	http.Redirect(w, r, url, http.StatusFound)
}

// DownloadChecksums redirects to the SHA256SUMS file for the latest release.
// GET /download/SHA256SUMS — public, no auth.
func (h *DistributionHandler) DownloadChecksums(w http.ResponseWriter, r *http.Request) {
	version := h.LatestVersion()
	url := fmt.Sprintf("%s/%s/SHA256SUMS", githubDownloadURL, version)
	http.Redirect(w, r, url, http.StatusFound)
}

// InstallScript serves a dynamically generated install script with the endpoint pre-baked.
// GET /install.sh — public, no auth.
func (h *DistributionHandler) InstallScript(w http.ResponseWriter, r *http.Request) {
	endpoint := h.Endpoint

	// Use request host as fallback if endpoint is localhost.
	if strings.Contains(endpoint, "localhost") || strings.Contains(endpoint, "127.0.0.1") {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		} else if TrustedProxy && r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		endpoint = fmt.Sprintf("%s://%s", scheme, r.Host)
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, generateInstallScript(endpoint))
}

func generateInstallScript(endpoint string) string {
	return `#!/usr/bin/env bash
# hopssh install script
# Usage:
#   curl -fsSL ` + endpoint + `/install.sh | sh                    # install hop-agent (default)
#   curl -fsSL ` + endpoint + `/install.sh | sh -s -- --server     # install hop-server
#   curl -fsSL ` + endpoint + `/install.sh | sh -s -- --all        # install both
#   curl -fsSL ` + endpoint + `/install.sh | sh -s -- --version v0.2.0  # specific version
set -euo pipefail

ENDPOINT="` + endpoint + `"
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
  VERSION=$(curl -fsSL "${ENDPOINT}/version" 2>/dev/null | grep -o '"version":"[^"]*"' | cut -d'"' -f4) || true
  if [ -z "$VERSION" ]; then
    echo "Error: Could not determine latest version from ${ENDPOINT}/version"
    echo "Try specifying a version: curl ... | sh -s -- --version v0.1.0"
    exit 1
  fi
fi

echo "==> Installing hopssh ${VERSION} (${OS}/${ARCH})"

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

install_binary() {
  local name="$1"
  local bin="hop-${name}-${OS}-${ARCH}"
  local url="${ENDPOINT}/download/${bin}"
  local tmpfile
  tmpfile=$(mktemp)

  echo "==> Downloading ${bin}..."
  if ! curl -fsSL "${url}" -o "${tmpfile}"; then
    rm -f "${tmpfile}"
    echo "Error: Failed to download ${bin} from ${url}"
    echo "Check that the control plane is running and the version exists."
    exit 1
  fi

  # Verify checksum
  local checksums
  checksums=$(curl -fsSL "${ENDPOINT}/download/SHA256SUMS" 2>/dev/null) || true
  if [ -n "$checksums" ]; then
    local expected
    expected=$(echo "$checksums" | grep "${bin}" | awk '{print $1}')
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
    echo ""
    echo "    To add a SERVER (VPS, NAS, Raspberry Pi):"
    echo "      sudo hop-agent enroll --endpoint ${ENDPOINT}"
    echo ""
    echo "    To join as a CLIENT (laptop, phone, personal device):"
    echo "      sudo hop-agent enroll --client --endpoint ${ENDPOINT}"
    echo ""
    echo "    With a token from the dashboard:"
    echo "      echo '<token>' | sudo hop-agent enroll --token-stdin --endpoint ${ENDPOINT}"
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
`
}
