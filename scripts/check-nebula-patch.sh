#!/usr/bin/env bash
# check-nebula-patch.sh — Checks if our vendor patch is still needed.
#
# We patch vendor/github.com/slackhq/nebula/interface.go to fix os.Exit(2)
# on service shutdown (issue #1031). Once upstream PR #1375 is merged and
# released, we should drop the patch and update to the new version.
#
# Run periodically (e.g., monthly) or add to CI.
# Requires: gh CLI authenticated, or GITHUB_TOKEN set.
#
# References:
#   - Issue: https://github.com/slackhq/nebula/issues/1031
#   - PR:    https://github.com/slackhq/nebula/pull/1375
#   - Patch: patches/nebula-1031-graceful-shutdown.patch
set -euo pipefail

PR_NUMBER=1375
REPO="slackhq/nebula"
CURRENT_VERSION="v1.10.3"

echo "==> Checking if vendor patch is still needed"

# Check if PR #1375 is merged.
PR_STATE=$(gh pr view "$PR_NUMBER" --repo "$REPO" --json state -q '.state' 2>/dev/null || echo "UNKNOWN")
echo "    PR #$PR_NUMBER state: $PR_STATE"

if [[ "$PR_STATE" == "MERGED" ]]; then
  echo ""
  echo "    ⚠️  PR #$PR_NUMBER has been MERGED upstream!"
  echo "    Action required:"
  echo "      1. Find the release: gh release list --repo $REPO --limit 5"
  echo "      2. Update: go get github.com/slackhq/nebula@<new-version>"
  echo "      3. Re-vendor: make vendor"
  echo "      4. Verify the patch is no longer needed (check interface.go)"
  echo "      5. If fixed upstream, remove patches/ and update Makefile"
  echo ""
  exit 1
fi

# Check for new upstream releases.
LATEST=$(gh release view --repo "$REPO" --json tagName -q '.tagName' 2>/dev/null || echo "UNKNOWN")
echo "    Current pinned: $CURRENT_VERSION"
echo "    Latest release: $LATEST"

if [[ "$LATEST" != "$CURRENT_VERSION" && "$LATEST" != "UNKNOWN" ]]; then
  echo ""
  echo "    ⚠️  New nebula release available: $LATEST (we're on $CURRENT_VERSION)"
  echo "    Check if it includes the fix from PR #1375."
  echo "    If yes: update version, re-vendor, remove patch."
  echo "    If no:  update version, re-vendor, re-apply patch."
  exit 1
fi

echo "    ✓ Patch still needed (PR #$PR_NUMBER is $PR_STATE)"
exit 0
