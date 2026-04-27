#!/usr/bin/env bash
# build-dmg.sh — Build a polished drag-to-Applications DMG for hopssh.
#
# Why we don't just use `tauri build --bundles dmg`:
#   - Tauri 2.10.x invokes `bundle_dmg.sh` (a fork of create-dmg) which runs an
#     AppleScript step to position icons inside the DMG window. On macOS
#     Sequoia (and in headless CI) that AppleScript times out with -1712
#     because the parent process lacks Automation->Finder permission.
#   - `bundle_dmg.sh` supports a `--sandbox-safe` flag that skips the
#     AppleScript entirely. Tauri's CLI doesn't expose a way to pass it.
#
# So this script:
#   1. Runs `tauri build --bundles app` to produce the signed .app
#   2. Calls Tauri's own `bundle_dmg.sh` with --sandbox-safe to produce the DMG
#
# The resulting DMG opens to a tiny window with hopssh.app on the left and an
# Applications symlink on the right (default Finder layout, since icon-position
# AppleScript was skipped — Finder snaps both icons to a clean grid which is
# perfectly legible for the drag-to-install flow).

set -euo pipefail

cd "$(dirname "$0")/.."

VERSION=$(node -p 'require("./package.json").version')
ARCH=$(uname -m)
case "$ARCH" in
  arm64) DMG_ARCH=aarch64 ;;
  x86_64) DMG_ARCH=x86_64 ;;
  *) echo "unsupported arch: $ARCH" >&2; exit 1 ;;
esac

DMG_NAME="hopssh_${VERSION}_${DMG_ARCH}.dmg"
BUNDLE_DIR="src-tauri/target/release/bundle"
APP_PATH="${BUNDLE_DIR}/macos/hopssh.app"
DMG_DIR="${BUNDLE_DIR}/dmg"
DMG_PATH="${DMG_DIR}/${DMG_NAME}"

echo "[build-dmg] Building hopssh.app + seeding DMG support files..."
# We pass --bundles app,dmg even though the dmg step will fail at the
# AppleScript stage. The failure is what seeds bundle_dmg.sh + icon.icns
# into target/release/bundle/dmg/, which we then drive ourselves below
# with --sandbox-safe (which Tauri's CLI doesn't expose).
npx tauri build --bundles app,dmg || true

if [[ ! -d "$APP_PATH" ]]; then
  echo "[build-dmg] ERROR: ${APP_PATH} not found after tauri build" >&2
  exit 1
fi

echo "[build-dmg] Preparing DMG staging directory..."
mkdir -p "$DMG_DIR"
rm -f "$DMG_PATH"
find "${BUNDLE_DIR}/macos" -name 'rw.*.dmg' -delete 2>/dev/null || true

STAGING="${DMG_DIR}/staging"
rm -rf "$STAGING"
mkdir "$STAGING"
cp -R "$APP_PATH" "$STAGING/"

# bundle_dmg.sh and icon.icns are written by Tauri's bundler. If a previous
# `tauri build --bundles dmg` (or `--bundles app,dmg`) ran, they exist; if not,
# we fall back to skipping the volume icon.
BUNDLE_DMG_SH="${DMG_DIR}/bundle_dmg.sh"
VOLICON="${DMG_DIR}/icon.icns"

if [[ ! -x "$BUNDLE_DMG_SH" ]]; then
  echo "[build-dmg] ERROR: ${BUNDLE_DMG_SH} not found." >&2
  echo "[build-dmg]   Run \`npx tauri build --bundles app,dmg\` once first to" >&2
  echo "[build-dmg]   let Tauri seed bundle_dmg.sh + icon.icns. The dmg step" >&2
  echo "[build-dmg]   will fail at the AppleScript stage; that's OK — this" >&2
  echo "[build-dmg]   script picks up where it left off with --sandbox-safe." >&2
  exit 1
fi

VOLICON_ARGS=()
if [[ -f "$VOLICON" ]]; then
  VOLICON_ARGS=(--volicon icon.icns)
fi

echo "[build-dmg] Running bundle_dmg.sh with --sandbox-safe..."
(
  cd "$DMG_DIR"
  ./bundle_dmg.sh \
    --sandbox-safe \
    --volname hopssh \
    --icon hopssh.app 140 200 \
    --app-drop-link 400 200 \
    --window-size 540 380 \
    --hide-extension hopssh.app \
    "${VOLICON_ARGS[@]}" \
    "$DMG_NAME" staging
)

rm -rf "$STAGING"

# Also produce a stable-named copy so the /download/desktop endpoint can
# redirect to a known URL without having to look up the desktop version.
STABLE_DMG_NAME="hopssh-macos-${DMG_ARCH}.dmg"
STABLE_DMG_PATH="${DMG_DIR}/${STABLE_DMG_NAME}"
cp "$DMG_PATH" "$STABLE_DMG_PATH"

echo "[build-dmg] DMG created:"
ls -lh "$DMG_PATH" "$STABLE_DMG_PATH"
