#!/usr/bin/env bash
# T2: Clean TCP throughput through a VPN tunnel.
#
# Usage (server side, once): iperf3 -s &
# Usage (client side):       ./iperf-tunnel.sh <vpn-name> <peer-vpn-ip>
#
# vpn-name is a label for the log file (hopssh | tailscale | ...).
# peer-vpn-ip is the peer's mesh overlay IP for that VPN.

set -euo pipefail

if [[ $# -ne 2 ]]; then
    echo "usage: $0 <vpn-name> <peer-vpn-ip>" >&2
    exit 1
fi

VPN="$1"
PEER="$2"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT_DIR="$(dirname "${SCRIPT_DIR}")"
OUT_FILE="${OUT_DIR}/02-iperf-${VPN}.log"

if ! command -v iperf3 >/dev/null; then
    echo "iperf3 not found. Install with: brew install iperf3" >&2
    exit 1
fi

DURATION=30
PARALLEL=4

echo "Running iperf3 through ${VPN} tunnel to ${PEER} for ${DURATION}s with ${PARALLEL} streams..."
echo "REMINDER: server peer must be running 'iperf3 -s' (default port 5201)"
echo ""

{
    echo "# T2 iperf3 through ${VPN} tunnel"
    echo "# Date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "# Peer VPN IP: ${PEER}"
    echo "# Duration: ${DURATION}s, Parallel streams: ${PARALLEL}"
    echo ""
    echo "## iperf3 client output"
    iperf3 -c "${PEER}" -t "${DURATION}" -P "${PARALLEL}" -i 5 2>&1 || true
    echo ""
    echo "## iperf3 reverse (download)"
    iperf3 -c "${PEER}" -t "${DURATION}" -P "${PARALLEL}" -i 5 -R 2>&1 || true
} | tee "${OUT_FILE}"

echo ""
echo "Written to: ${OUT_FILE}"
