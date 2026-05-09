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
	// versionCacheTTL — Phase CC (v0.10.95): reduced from 5min to 60s.
	// GitHub's unauthenticated API rate limit is 60/h; one call/min stays
	// well within budget while keeping `Latest available:` fresh for
	// users hitting Check-for-updates moments after a release ships.
	versionCacheTTL = 60 * time.Second
)

// validBinaryName matches hop-agent-linux-amd64, hop-server-darwin-arm64, etc.
var validBinaryName = regexp.MustCompile(`^hop-(agent|server)-(linux|darwin|windows)-(amd64|arm64)(\.exe)?$`)

// validDesktopAsset matches stable per-platform desktop filenames.
// We ship one file per (os, arch) and the os dictates the file extension:
//   macos   -> .dmg
//   windows -> .exe
//   linux   -> .AppImage
// validDesktopAsset accepts the desktop bundles served by /download/desktop/{asset}.
// macOS dropped Intel (x86_64) on 2026-05-09 — Apple Silicon only.
// Windows MSI + Linux .deb/.rpm/AppImage land here once the build-windows /
// build-linux jobs in release-desktop.yml emit them with these stable names.
var validDesktopAsset = regexp.MustCompile(`^hopssh-(?:macos-aarch64\.dmg|windows-(?:x86_64|aarch64)(?:\.msi|-setup\.exe)|linux-x86_64\.(?:AppImage|deb|rpm))$`)

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
//
// Phase CC (v0.10.95): the `version` field is now max(current, fetched)
// rather than just `fetched`. Rationale: if THIS container is running
// tag X, then X has been built AND published as a Docker image — so X
// is by definition a valid "latest" available. This eliminates the
// confusing "Latest available: v0.10.93" while running v0.10.94 window
// (~8 minutes after a tag bump while GitHub's /releases/latest catches
// up + our 60s cache expires). Without this, users see "Latest
// available" appear to regress for a few minutes after each release.
func (h *DistributionHandler) Version(w http.ResponseWriter, r *http.Request) {
	fetched := h.LatestVersion()
	current := buildinfo.Version
	latest := pickNewerVersion(current, fetched)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"version": latest,
		"current": current,
	})
}

// pickNewerVersion returns whichever of the two versions parses as
// semver-newer. Falls back to `a` when either side fails to parse —
// preserves the user's running version as a safe default.
//
// Strips leading `v` and any `-suffix` (e.g. `-dirty`, `-rc1`) before
// comparing the numeric components. So `v0.10.94-dirty` compares as
// `0.10.94` against `v0.10.93` → returns `v0.10.94-dirty` (the
// suffix is preserved on the returned string, only the comparison
// strips it).
//
// Returns `a` on tie. Returns `a` on parse failure of either side.
func pickNewerVersion(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	pa, okA := parseSemverComponents(a)
	pb, okB := parseSemverComponents(b)
	if !okA || !okB {
		return a
	}
	for i := 0; i < 3; i++ {
		if pa[i] > pb[i] {
			return a
		}
		if pa[i] < pb[i] {
			return b
		}
	}
	return a // exact tie
}

// parseSemverComponents extracts the [major, minor, patch] integer
// triple from a tag string like `v0.10.94-dirty`. Returns (triple,
// true) on success, ([0,0,0], false) on parse failure.
func parseSemverComponents(s string) ([3]int, bool) {
	var out [3]int
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s = s[:i]
	}
	parts := strings.SplitN(s, ".", 4)
	if len(parts) < 3 {
		return out, false
	}
	for i := 0; i < 3; i++ {
		n, err := parseUint(parts[i])
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// parseUint is fmt.Sscanf-free; just digits.
func parseUint(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("non-digit %q", c)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
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

// DownloadDesktop redirects to the desktop client asset (DMG, exe, AppImage)
// for the requested platform on the latest release.
// GET /download/desktop/{asset} — public, no auth.
//
// asset is one of: hopssh-macos-aarch64.dmg, hopssh-macos-x86_64.dmg,
// hopssh-windows-x86_64.exe, hopssh-linux-x86_64.AppImage, etc.
func (h *DistributionHandler) DownloadDesktop(w http.ResponseWriter, r *http.Request) {
	asset := chi.URLParam(r, "asset")
	if !validDesktopAsset.MatchString(asset) {
		http.Error(w, "Invalid desktop asset name. Expected one of: hopssh-macos-aarch64.dmg, hopssh-windows-{x86_64|aarch64}.msi, hopssh-windows-{x86_64|aarch64}-setup.exe, hopssh-linux-x86_64.{AppImage|deb|rpm}", http.StatusBadRequest)
		return
	}
	version := h.LatestVersion()
	url := fmt.Sprintf("%s/%s/%s", githubDownloadURL, version, asset)
	http.Redirect(w, r, url, http.StatusFound)
}

// InstallDesktopScript serves a curl-pipeable shell installer for the
// macOS .app. Bypasses the Gatekeeper notarization wall on macOS
// Sequoia: files downloaded via curl never get the com.apple.quarantine
// xattr (only browsers set it), so an ad-hoc-signed .app installed
// this way launches without the "Apple could not verify..." dialog.
//
// GET /install-mac.sh — public, no auth.
//
// Usage from the user's terminal:
//
//	curl -fsSL https://hopssh.com/install-mac.sh | bash
func (h *DistributionHandler) InstallDesktopScript(w http.ResponseWriter, r *http.Request) {
	endpoint := h.Endpoint
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
	fmt.Fprint(w, generateMacInstallScript(endpoint))
}

// generateMacInstallScript builds a bash one-liner that downloads the
// DMG, mounts it, copies hopssh.app into /Applications, ejects the
// DMG, strips quarantine, and launches the app. Because the script
// runs from the user's terminal (curl-fetched), the DMG download
// itself goes via curl too — no quarantine xattr ever gets set.
func generateMacInstallScript(endpoint string) string {
	return `#!/usr/bin/env bash
# hopssh macOS installer — bypasses Gatekeeper notarization gate by
# downloading + installing via curl (which doesn't set quarantine
# xattrs the way browsers do).
#
# Usage:  curl -fsSL ` + endpoint + `/install-mac.sh | bash
set -euo pipefail

OS=$(uname -s)
if [ "$OS" != "Darwin" ]; then
  echo "Error: this installer is macOS-only." >&2
  exit 1
fi

ARCH=$(uname -m)
case "$ARCH" in
  arm64)  ASSET="hopssh-macos-aarch64.dmg" ;;
  x86_64)
    cat <<'EOF' >&2
Error: hopssh dropped Intel macOS support on 2026-05-09.
The desktop client now ships Apple Silicon only.

If you have an Intel Mac, the CLI agent still works:
  curl -fsSL ` + endpoint + `/install.sh | sudo bash

Run the dashboard from any browser. We'll consider re-adding the
Intel desktop build if there's verifiable demand — file an issue at
https://github.com/trustos/hopssh/issues with your use case.
EOF
    exit 1 ;;
  *) echo "Error: unsupported arch $ARCH" >&2; exit 1 ;;
esac

# Prompt for the admin password UP FRONT so failures are loud and
# happen before we've downloaded a 25 MB DMG. Without this, the first
# sudo call lands at the rm/ditto step mid-script — if the user has
# wandered away from the terminal, sudo's 5s default Touch ID timeout
# silently fails, the script aborts, and prior installs never see the
# new bundle. We've watched this fail repeatedly in the field.
echo "==> Requesting admin password (needed to write to /Applications)..."
if ! sudo -v; then
  echo "Error: admin password required to install into /Applications. Aborting." >&2
  exit 1
fi

# Keep sudo's credential cache warm for the duration of the script
# (default 5 min timeout otherwise). Background job — killed on EXIT.
# disown removes the job from bash's job table so the eventual
# kill-on-EXIT does NOT print a "Terminated: 15" notice to the
# user's Terminal — cosmetic noise from job control that looks
# like an install failure to non-technical users.
( while true; do sudo -n true 2>/dev/null; sleep 30; done ) &
SUDO_KEEPALIVE_PID=$!
disown $SUDO_KEEPALIVE_PID 2>/dev/null || true

TMPDMG=$(mktemp -t hopssh-XXXXXX.dmg)
trap "kill $SUDO_KEEPALIVE_PID 2>/dev/null || true; rm -f $TMPDMG; hdiutil detach /Volumes/hopssh 2>/dev/null || true" EXIT

echo "==> Downloading $ASSET..."
if ! curl -fsSL "` + endpoint + `/download/desktop/$ASSET" -o "$TMPDMG"; then
  echo "Error: failed to download $ASSET from ` + endpoint + `" >&2
  exit 1
fi

echo "==> Mounting DMG..."
if ! hdiutil attach "$TMPDMG" -nobrowse -quiet; then
  echo "Error: failed to mount DMG" >&2
  exit 1
fi

echo "==> Installing to /Applications..."
HAD_PRIOR_INSTALL=0
if [ -d /Applications/hopssh.app ]; then
  HAD_PRIOR_INSTALL=1
  killall hopssh-desktop 2>/dev/null || true
  sleep 1
  if ! sudo rm -rf /Applications/hopssh.app; then
    echo "Error: failed to remove existing /Applications/hopssh.app — install aborted" >&2
    exit 1
  fi
fi
if ! sudo /usr/bin/ditto --noextattr /Volumes/hopssh/hopssh.app /Applications/hopssh.app; then
  echo "Error: failed to copy hopssh.app into /Applications" >&2
  exit 1
fi

# Phase J: when the user previously enabled "Run in the background"
# (Settings → Preferences), a LaunchDaemon at /Library/LaunchDaemons/
# com.hopssh.agent.plist runs /usr/local/bin/hop-agent as root. The
# .app updater only refreshes the bundle's child binary — the system
# binary stays at whatever version the user converted at. Without
# this step, system-mode users keep seeing the OLD agent version in
# Settings → Updates even after a fresh install. Refresh both.
if [ -f /Library/LaunchDaemons/com.hopssh.agent.plist ]; then
  echo "==> Refreshing system-mode hop-agent (LaunchDaemon detected)..."
  if ! sudo cp /Applications/hopssh.app/Contents/Resources/binaries/hop-agent /usr/local/bin/hop-agent; then
    echo "Warning: failed to refresh /usr/local/bin/hop-agent — system mode may report stale version" >&2
  else
    sudo launchctl kickstart -k system/com.hopssh.agent 2>/dev/null || true
  fi
fi

echo "==> Ejecting DMG..."
hdiutil detach /Volumes/hopssh -quiet

echo "==> Removing any quarantine xattr (defense-in-depth)..."
sudo xattr -cr /Applications/hopssh.app 2>/dev/null || true

# When replacing an existing install we just SIGKILL'd the running .app
# above. macOS's SystemUIServer doesn't always reap the dead process's
# NSStatusItem; the ghost survives until logout/reboot, and the new
# .app's tray icon registers alongside it — user sees TWO icons. Flush
# the cache by restarting SystemUIServer + ControlCenter (both daemons
# auto-restart via launchd in <5s with a fresh menubar that contains
# only currently-registered status items).
#
# Skipped on first install (HAD_PRIOR_INSTALL=0) because there's no
# stale registration to flush and the restart blanks the user's whole
# menubar for ~3s — not worth it for the no-ghost case.
if [ "$HAD_PRIOR_INSTALL" = 1 ]; then
  echo "==> Flushing menubar (clears the prior install's tray ghost)..."
  killall SystemUIServer 2>/dev/null || true
  killall ControlCenter 2>/dev/null || true
  sleep 2
fi

echo "==> Launching hopssh..."
open /Applications/hopssh.app

echo ""
echo "Done. hopssh is now running. Click the menubar icon to get started."
`
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
    # Check if already enrolled — show different message for updates vs first install.
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
      echo "      sudo hop-agent enroll --endpoint ${ENDPOINT}"
      echo ""
      echo "    Or with a token from the dashboard:"
      echo "      echo '<token>' | sudo hop-agent enroll --token-stdin --endpoint ${ENDPOINT}"
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
`
}
