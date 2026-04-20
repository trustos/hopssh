#!/usr/bin/env bash
# Run the full benchmark harness end-to-end.
#
# Prerequisites (check before running):
#   1. Both VPNs active on both peers (check `tailscale status` and hopssh dashboard)
#   2. iperf3 server running on the REMOTE peer: iperf3 -s
#   3. Force-relay mode enabled on both peers (see ../README.md)
#
# Usage: ./run-all.sh <peer-hopssh-ip> <peer-tailscale-ip> [hopssh-relay-host]

set -euo pipefail

if [[ $# -lt 2 ]]; then
    echo "usage: $0 <peer-hopssh-ip> <peer-tailscale-ip> [hopssh-relay-host]" >&2
    exit 1
fi

PEER_HOPSSH="$1"
PEER_TS="$2"
HOPSSH_RELAY="${3:-}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "=================================================="
echo "hopssh vs Tailscale relay benchmark"
echo "=================================================="
echo "  hopssh peer   : ${PEER_HOPSSH}"
echo "  tailscale peer: ${PEER_TS}"
echo "  hopssh relay  : ${HOPSSH_RELAY:-auto-detect}"
echo "=================================================="
echo ""

echo ">>> T1: Path RTT"
if [[ -n "${HOPSSH_RELAY}" ]]; then
    "${SCRIPT_DIR}/path-rtt.sh" "${HOPSSH_RELAY}"
else
    "${SCRIPT_DIR}/path-rtt.sh"
fi

echo ""
echo ">>> T2a: iperf3 through hopssh"
"${SCRIPT_DIR}/iperf-tunnel.sh" hopssh "${PEER_HOPSSH}"

echo ""
echo ">>> T2b: iperf3 through Tailscale"
"${SCRIPT_DIR}/iperf-tunnel.sh" tailscale "${PEER_TS}"

echo ""
echo ">>> T3a: TCP-RTT under load via hopssh"
"${SCRIPT_DIR}/tcp-rtt-load.sh" hopssh "${PEER_HOPSSH}"

echo ""
echo ">>> T3b: TCP-RTT under load via Tailscale"
"${SCRIPT_DIR}/tcp-rtt-load.sh" tailscale "${PEER_TS}"

echo ""
echo "=================================================="
echo "Automated tests complete. For T5 (real screen-sharing),"
echo "run each manually:"
echo ""
echo "  sudo ${SCRIPT_DIR}/capture-screenshare.sh hopssh    ${PEER_HOPSSH} 60"
echo "  sudo ${SCRIPT_DIR}/capture-screenshare.sh tailscale ${PEER_TS} 60"
echo ""
echo "Then fill in: $(dirname "${SCRIPT_DIR}")/RESULTS.md"
echo "=================================================="
