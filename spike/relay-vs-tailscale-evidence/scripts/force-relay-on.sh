#!/usr/bin/env bash
# Force-relay mode: blocks direct UDP to the peer so the VPN falls back
# to its relay. Uses pfctl; leaves existing rules intact by installing
# into a named anchor referenced from /etc/pf.conf.
#
# Usage: sudo ./force-relay-on.sh <peer-physical-ip>
#
# Verify after running:
#   hopssh:    dashboard peer row should say 'relayed'
#   tailscale: `tailscale status` should show 'via DERP <region>'
#
# Remove with: sudo ./force-relay-off.sh

set -euo pipefail

if [[ $# -ne 1 ]]; then
    echo "usage: sudo $0 <peer-physical-ip>" >&2
    exit 1
fi

if [[ $EUID -ne 0 ]]; then
    echo "must be run as root (sudo)" >&2
    exit 1
fi

PEER="$1"
ANCHOR="hopssh-bench"
ANCHOR_FILE="/etc/pf.anchors/${ANCHOR}"
PF_CONF_BACKUP="/etc/pf.conf.hopssh-bench.bak"

# Write our block rules into the anchor file.
# - hopssh listens on UDP 4242 (nebulacfg.ListenPort).
# - Tailscale uses UDP 41641 (default listen port) and random high UDP ports
#   discovered via DERP peer endpoints. We can't know the random port ahead
#   of time, so we block UDP to the peer IP broadly on ports above 1024.
#   This is fine because we do NOT block TCP — control-plane traffic + DERP
#   (TLS/443) keep working.
cat > "${ANCHOR_FILE}" <<EOF
block drop out proto udp from any to ${PEER}
block drop in  proto udp from ${PEER} to any
EOF

# Reference the anchor from pf.conf if not already referenced.
if ! grep -q "anchor \"${ANCHOR}\"" /etc/pf.conf; then
    cp /etc/pf.conf "${PF_CONF_BACKUP}"
    {
        cat /etc/pf.conf
        echo ""
        echo "# installed by hopssh spike/relay-vs-tailscale-evidence/force-relay-on.sh"
        echo "anchor \"${ANCHOR}\""
        echo "load anchor \"${ANCHOR}\" from \"${ANCHOR_FILE}\""
    } > /etc/pf.conf.new
    mv /etc/pf.conf.new /etc/pf.conf
fi

# Reload pf with the updated conf, enable if not already
pfctl -f /etc/pf.conf 2>&1 | grep -v "No ALTQ support" || true
pfctl -e 2>/dev/null || true

echo "✓ pfctl anchor '${ANCHOR}' loaded — direct UDP to/from ${PEER} blocked"
echo ""
echo "Verify relay fallback:"
echo "  hopssh:    open dashboard, peer's row should show 'relayed'"
echo "  tailscale: tailscale status | grep -i derp"
echo ""
echo "If the VPN doesn't fall back within ~15 s, cycle the agents:"
echo "  sudo launchctl kickstart -k system/com.trustos.hopssh.agent"
echo "  sudo tailscale down && sudo tailscale up"
echo ""
echo "Remove block: sudo $(dirname "$0")/force-relay-off.sh"
