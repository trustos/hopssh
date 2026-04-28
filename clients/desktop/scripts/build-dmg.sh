#!/usr/bin/env bash
# build-dmg.sh — Build a polished drag-to-Applications DMG for hopssh.
#
# Why we don't use `tauri build --bundles dmg`:
#   - Tauri 2.10.x invokes `bundle_dmg.sh` (forked from create-dmg) which
#     runs Finder AppleScript to position icons, set the background, etc.
#     On macOS Sequoia AND on `macos-latest` GitHub runners that
#     AppleScript times out with -1712 (Automation->Finder denied).
#   - Even with --sandbox-safe (skip AppleScript) the result has no
#     window size, no icon positions, no background — just a default
#     Finder window. Users complain (rightly) that it doesn't look like
#     a real installer.
#
# So this script:
#   1. Runs `tauri build --bundles app` to produce the .app
#   2. Properly ad-hoc-signs the .app bundle (creates _CodeSignature/
#      CodeResources). Without this, downloaded-from-browser DMGs fail
#      with "hopssh is damaged and can't be opened" because Gatekeeper
#      rejects bundles that have signed Mach-O binaries but no sealed
#      resources. With it, users see "unidentified developer" and can
#      bypass once via right-click -> Open.
#   3. Uses the `appdmg` npm tool (pure Node.js, no Finder Automation)
#      to build the DMG with our background image, icon positions, and
#      window size.

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
STABLE_DMG_NAME="hopssh-macos-${DMG_ARCH}.dmg"
BUNDLE_DIR="src-tauri/target/release/bundle"
APP_PATH="${BUNDLE_DIR}/macos/hopssh.app"
DMG_DIR="${BUNDLE_DIR}/dmg"
DMG_PATH="${DMG_DIR}/${DMG_NAME}"
STABLE_DMG_PATH="${DMG_DIR}/${STABLE_DMG_NAME}"

echo "[build-dmg] Building hopssh.app..."
npx tauri build --bundles app

if [[ ! -d "$APP_PATH" ]]; then
  echo "[build-dmg] ERROR: ${APP_PATH} not found after tauri build" >&2
  exit 1
fi

# Ad-hoc-sign the bundle. Critical layering for downloaded-and-quarantined
# bundles to actually launch:
#
#   1. Inner Mach-O binaries inside Contents/Resources/binaries/ get a
#      proper ad-hoc signature instead of the linker's auto-generated
#      "linker-signed" stub. macOS treats linker-signed binaries inside
#      a quarantined bundle as untrusted and silently blocks Launch
#      Services from spawning them as children. A real ad-hoc codesign
#      passes the post-quarantine spawn check after user approves the
#      parent .app once.
#   2. Then --deep-sign the bundle, which seals _CodeSignature/Code
#      Resources. Without this, Gatekeeper says "is damaged and can't
#      be opened" with no bypass option (we shipped this fix for the
#      .app itself; the inner-binary fix above completes the picture).
#
# If APPLE_SIGNING_IDENTITY is set (CI signed path), skip — Tauri or a
# later step will sign with the real cert.
if [[ -z "${APPLE_SIGNING_IDENTITY:-}" ]]; then
  echo "[build-dmg] Ad-hoc signing inner binaries (replaces linker-signed)..."
  if [[ -f "$APP_PATH/Contents/Resources/binaries/hop-agent" ]]; then
    codesign --force --sign - "$APP_PATH/Contents/Resources/binaries/hop-agent"
  fi
  echo "[build-dmg] Ad-hoc deep-signing the .app bundle..."
  codesign --force --deep --sign - "$APP_PATH"
  codesign -dv --verbose=2 "$APP_PATH" 2>&1 | grep -E 'Sealed|Signature|Identifier' || true
  # Verify the inner binary picked up a real ad-hoc signature, not
  # linker-signed. If this assertion ever fails, downloads will fail
  # again with "agent unreachable" and the user gets a regression.
  echo "[build-dmg] Verifying inner binary signature..."
  if codesign -dv --verbose=2 "$APP_PATH/Contents/Resources/binaries/hop-agent" 2>&1 | grep -q "linker-signed"; then
    echo "[build-dmg] ERROR: hop-agent inside the bundle is still linker-signed —" >&2
    echo "[build-dmg] post-download Gatekeeper would block the spawn. Aborting." >&2
    exit 1
  fi
fi

echo "[build-dmg] Cleaning DMG output dir..."
mkdir -p "$DMG_DIR"
rm -f "$DMG_PATH" "$STABLE_DMG_PATH"

echo "[build-dmg] Building DMG with appdmg..."
npx appdmg scripts/dmg-spec.json "$DMG_PATH"

cp "$DMG_PATH" "$STABLE_DMG_PATH"

echo "[build-dmg] DMG created:"
ls -lh "$DMG_PATH" "$STABLE_DMG_PATH"
