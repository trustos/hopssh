#!/usr/bin/env bash
# dev-deploy-desktop.sh — Build and deploy the macOS .app to both Macs
# for local testing. Mirror of dev-deploy.sh (which ships hop-agent CLI
# only) but for the Tauri desktop client.
#
# What it does end-to-end:
#   1. Build a universal hop-agent binary (arm64 + amd64 lipo).
#   2. Build the Tauri .app (npx tauri build --bundles app), which
#      embeds the SPA via tauri.conf.json's beforeBuildCommand.
#   3. Replace the bundled hop-agent inside the .app with the just-
#      built universal binary.
#   4. Re-sign the inner binary with a real ad-hoc signature (not
#      linker-signed) AND deep-sign the bundle (creates _CodeSignature/
#      CodeResources). Without this, downloaded bundles trigger
#      Gatekeeper "is damaged" — and freshly-spawned children get
#      blocked under quarantine. See clients/desktop/scripts/build-
#      dmg.sh for the full reasoning.
#   5. Tar + install on local Mac mini (this host) AND scp + install
#      on the laptop (via SSH). Each install: stop running app, rm
#      -rf /Applications/hopssh.app, untar, xattr -cr (strip
#      quarantine), open.
#
# Usage:
#   make dev-deploy-desktop
#
# Override env for your own dev setup:
#   LAPTOP_USER=yavortenev
#   LAPTOP_HOST=100.107.90.106  (Tailscale; survives WiFi changes)
#   SKIP_LAPTOP=1               (skip the SSH leg entirely)
#   SKIP_LOCAL=1                (skip installing on this Mac)

set -euo pipefail

LAPTOP_USER="${LAPTOP_USER:-yavortenev}"
LAPTOP_HOST="${LAPTOP_HOST:-100.107.90.106}"
LAPTOP_SSH="${LAPTOP_USER}@${LAPTOP_HOST}"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DESKTOP="${REPO_ROOT}/clients/desktop"
APP_PATH="${DESKTOP}/src-tauri/target/release/bundle/macos/hopssh.app"
TARBALL="/tmp/hopssh-app-dev-$(date +%s).tar.gz"

echo "==> Building universal hop-agent (arm64 + amd64 lipo)..."
cd "${REPO_ROOT}"
# ClientType=desktop tells the control plane this binary is the .app
# sidecar so the dashboard's Nodes tab can render "🖥 Desktop" instead
# of "⌨ CLI". CLI builds via `make build` leave it empty (server
# defaults to "cli" for empty/unknown).
DESKTOP_LDFLAGS="-s -w -X github.com/trustos/hopssh/internal/buildinfo.ClientType=desktop"
GOOS=darwin GOARCH=arm64 go build -mod=vendor -ldflags="${DESKTOP_LDFLAGS}" \
    -o /tmp/hop-agent-darwin-arm64 ./cmd/agent
GOOS=darwin GOARCH=amd64 go build -mod=vendor -ldflags="${DESKTOP_LDFLAGS}" \
    -o /tmp/hop-agent-darwin-amd64 ./cmd/agent
lipo -create -output /tmp/hop-agent-universal \
    /tmp/hop-agent-darwin-arm64 /tmp/hop-agent-darwin-amd64

# Tauri's bundler picks up the sidecar from src-tauri/binaries/hop-agent
# (path is in tauri.conf.json bundle.resources). Ensure the latest is
# there BEFORE the .app build; the codesign step below also re-signs
# the bundled copy, but seeding here is what makes a fresh build pick
# up agent code changes.
mkdir -p "${DESKTOP}/src-tauri/binaries"
cp /tmp/hop-agent-universal "${DESKTOP}/src-tauri/binaries/hop-agent"

echo "==> Building Tauri .app..."
cd "${DESKTOP}"
npx tauri build --bundles app

if [[ ! -d "$APP_PATH" ]]; then
    echo "ERROR: ${APP_PATH} not found after tauri build" >&2
    exit 1
fi

echo "==> Re-signing bundled binary + deep-signing .app..."
# The Rust linker auto-applies a "linker-signed" stub to the universal
# binary; that's a SUBTYPE of ad-hoc that Gatekeeper distrusts under
# quarantine. Replace with a real ad-hoc signature, then deep-sign the
# bundle so _CodeSignature/CodeResources gets created. Both steps are
# necessary; deep-sign alone doesn't replace linker-signed inner
# binaries on macOS.
codesign --force --sign - "${APP_PATH}/Contents/Resources/binaries/hop-agent"
codesign --force --deep --sign - "${APP_PATH}"

echo "==> Tarballing .app..."
(cd "$(dirname "$APP_PATH")" && tar -czf "$TARBALL" hopssh.app)
echo "    -> $TARBALL ($(du -h "$TARBALL" | awk '{print $1}'))"

install_locally() {
    if [[ "${SKIP_LOCAL:-0}" == "1" ]]; then
        echo "==> Skipping local install (SKIP_LOCAL=1)"
        return
    fi
    echo "==> Installing on Mac mini (local)..."
    killall hopssh-desktop 2>/dev/null || true
    sleep 1
    sudo rm -rf /Applications/hopssh.app
    sudo tar -xzf "$TARBALL" -C /Applications/
    sudo xattr -cr /Applications/hopssh.app
    open /Applications/hopssh.app
    echo "    Mac mini: installed + opened"
}

install_remote() {
    if [[ "${SKIP_LAPTOP:-0}" == "1" ]]; then
        echo "==> Skipping laptop install (SKIP_LAPTOP=1)"
        return
    fi
    echo "==> Installing on laptop (${LAPTOP_HOST})..."
    if ! ssh -o ConnectTimeout=8 "${LAPTOP_SSH}" 'echo ok' >/dev/null 2>&1; then
        echo "    WARNING: ${LAPTOP_HOST} unreachable; skipping" >&2
        return
    fi
    scp -q "$TARBALL" "${LAPTOP_SSH}:/tmp/hopssh-app-dev.tar.gz"
    ssh "${LAPTOP_SSH}" '
        killall hopssh-desktop 2>/dev/null || true
        sleep 1
        sudo rm -rf /Applications/hopssh.app
        sudo tar -xzf /tmp/hopssh-app-dev.tar.gz -C /Applications/
        sudo xattr -cr /Applications/hopssh.app
        open /Applications/hopssh.app
        echo "    Laptop: installed + opened"
    '
}

install_locally
install_remote

echo "==> Done. Tarball: $TARBALL"
echo "    Both Macs are running the dev build of /Applications/hopssh.app."
echo "    The agent inside the .app is the freshly-built universal hop-agent."
