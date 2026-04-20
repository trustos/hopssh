#!/usr/bin/env bash
# Removes the pfctl force-relay anchor and restores the original /etc/pf.conf
# if this script installed modifications there.

set -euo pipefail

if [[ $EUID -ne 0 ]]; then
    echo "must be run as root (sudo)" >&2
    exit 1
fi

ANCHOR="hopssh-bench"
ANCHOR_FILE="/etc/pf.anchors/${ANCHOR}"
PF_CONF_BACKUP="/etc/pf.conf.hopssh-bench.bak"

# Empty the anchor
pfctl -a "${ANCHOR}" -F all 2>/dev/null || true

# Restore original pf.conf if we modified it
if [[ -f "${PF_CONF_BACKUP}" ]]; then
    mv "${PF_CONF_BACKUP}" /etc/pf.conf
    pfctl -f /etc/pf.conf 2>&1 | grep -v "No ALTQ support" || true
fi

rm -f "${ANCHOR_FILE}"

echo "✓ pfctl anchor '${ANCHOR}' removed — direct UDP no longer blocked"
echo ""
echo "The VPN may take ~15 s to re-establish direct P2P. Verify on the dashboard."
