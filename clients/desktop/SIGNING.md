# Code signing + notarization + auto-update setup

This is a one-time operator runbook for getting the macOS Tauri app
signed, notarized, packaged as a DMG, and equipped with auto-update.
The `.github/workflows/release-desktop.yml` workflow runs end-to-end
once these credentials are in place; until they are, the workflow's
preflight job emits a clean "skipped" warning and exits successfully
so CI green-status isn't held hostage by missing secrets.

Until this is done, users get an unsigned `.app` bundle (built locally
via `make desktop-app`). That works but Gatekeeper shows
"unidentified developer" the first time and requires Cmd-click → Open.

---

## What you need

1. **Apple Developer Program account** — $99/year. Apply at
   <https://developer.apple.com/programs/>. New-org review can take
   1–4 weeks; budget accordingly. Personal accounts (Individual
   tier) work but the cert says your name not "hopssh", which looks
   amateur in the Gatekeeper dialog.
2. **Developer ID Application certificate** — issued by Apple after
   the account is approved. Generate from
   <https://developer.apple.com/account/resources/certificates>.
   This is the **distribution** cert, not the Mac App Store one;
   we bypass the App Store entirely.
3. **App-specific password for `notarytool`** — generate at
   <https://account.apple.com/account/manage> → "App-Specific
   Passwords". Used so we don't store your real Apple-ID password.
4. **Tauri updater Ed25519 keypair** — generate locally:
   ```bash
   cd clients/desktop
   npx tauri signer generate -w ~/.hopssh-updater-key.pem
   ```
   Stores private key with a passphrase you choose. Never commit it.

---

## Adding the secrets to GitHub Actions

Repository → Settings → Secrets and variables → Actions → New repository
secret. Add EACH of these (case-sensitive name):

| Secret name | Value |
|---|---|
| `APPLE_DEVELOPER_ID_CERT_P12` | base64-encoded `.p12` of the cert: `base64 -i certificate.p12 \| pbcopy` then paste |
| `APPLE_DEVELOPER_ID_CERT_PASS` | password used when exporting the `.p12` |
| `APPLE_NOTARY_APPLE_ID` | the Apple ID email |
| `APPLE_NOTARY_TEAM_ID` | 10-char Team ID from the developer portal |
| `APPLE_NOTARY_APP_PASSWORD` | the app-specific password for notarytool |
| `TAURI_UPDATER_PRIVATE_KEY` | contents of `~/.hopssh-updater-key.pem` (the private key) |
| `TAURI_UPDATER_KEY_PASSWORD` | passphrase for the updater key |

Once all 7 are present, the next pushed `v*` tag triggers the full
sign + notarize + DMG + upload-to-release pipeline.

---

## Embedding the updater pubkey

Replace the placeholder pubkey in `src-tauri/tauri.conf.json` with
the one from your generated `.pub` file (`~/.hopssh-updater-key.pem.pub`):

```bash
cat ~/.hopssh-updater-key.pem.pub
# Paste into tauri.conf.json's plugins.updater.pubkey
```

The pubkey is non-secret — committing it is fine and required (the
update mechanism uses it to verify each new download). The PRIVATE
key stays in your password manager + the GitHub Actions secret.

---

## Updater manifest hosting

The Tauri updater fetches a JSON manifest at
`https://releases.hopssh.com/<target>/<version>` to discover new
releases. The release-desktop workflow uploads the artefacts to
the GitHub release; you'll need a small wrapper service (or a
Cloudflare Worker, or a static-site cron) that translates the
GitHub release feed into the manifest shape Tauri expects:

```json
{
  "version": "v0.10.38",
  "notes": "...",
  "pub_date": "2026-04-27T00:00:00Z",
  "platforms": {
    "darwin-aarch64": {
      "signature": "<base64 sig>",
      "url": "https://github.com/trustos/hopssh/releases/download/v0.10.38/hopssh.app.tar.gz"
    },
    "darwin-x86_64": {
      "signature": "<base64 sig>",
      "url": "..."
    }
  }
}
```

This wrapper is out of scope for the base scaffold; cleanest
approach is a Cloudflare Worker that calls the GitHub Releases API
and returns the manifest. ~50 lines of TypeScript.

---

## Smoke verification (post-first-signed-release)

After the workflow completes for the first signed tag:

1. Download the DMG from the GitHub release.
2. Open it on a fresh Mac that doesn't trust your Developer ID yet.
3. Drag to Applications, double-click. Gatekeeper should NOT show
   "unidentified developer" — only the standard "downloaded from
   internet" warning, which goes away after first launch.
4. `codesign --verify --deep --strict --verbose=2 /Applications/hopssh.app`
5. `spctl --assess --verbose=4 /Applications/hopssh.app`
6. `xcrun stapler validate /Applications/hopssh.app`
7. All three commands should exit 0 with no errors.

---

## When the cert is missing

The `release-desktop.yml` workflow's `preflight` job detects missing
secrets and emits a CI warning instead of failing. So:

- **No Apple Developer account yet**: CI runs preflight, prints "skipping signed-build job", exits green. The `release.yml` workflow continues to ship the cross-platform CLI binaries unaffected.
- **Apple account in review**: same — preflight skips silently.
- **Approved + cert obtained but not yet uploaded as secrets**: same as above.
- **All secrets present**: full sign + notarize + DMG happens.

This means committing the workflow scaffold today is safe even though
no signing happens yet. The moment the secrets land, the next tag
triggers a full signed release.
