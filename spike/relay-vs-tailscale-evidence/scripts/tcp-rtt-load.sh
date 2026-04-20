#!/usr/bin/env bash
# T3: TCP RTT distribution while the tunnel is under sustained load.
#
# Runs the tcp-rtt-probe tool (50 ms cadence, 3 min) while a background
# iperf3 flow fills the tunnel at 10 Mbps. The probe measures connect()
# time to a closed port on the peer — a clean proxy for interactive-packet
# round-trip time without ICMP's kernel-fast-path advantage.
#
# Usage: ./tcp-rtt-load.sh <vpn-name> <peer-vpn-ip>
#
# Requires: iperf3 server running on peer, go (to build the probe).

set -euo pipefail

if [[ $# -ne 2 ]]; then
    echo "usage: $0 <vpn-name> <peer-vpn-ip>" >&2
    exit 1
fi

VPN="$1"
PEER="$2"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "${SCRIPT_DIR}")"
OUT_FILE="${ROOT_DIR}/03-tcp-rtt-${VPN}.log"
PROBE_DIR="${ROOT_DIR}/tools/tcp-rtt-probe"
PROBE_BIN="${PROBE_DIR}/tcp-rtt-probe"

DURATION=180
CADENCE_MS=50
BG_BITRATE="10M"

# Build probe if missing/stale
(cd "${PROBE_DIR}" && go build -o tcp-rtt-probe .)

echo "Running TCP-RTT load test through ${VPN} to ${PEER}"
echo "  probe cadence : ${CADENCE_MS} ms"
echo "  probe duration: ${DURATION}s"
echo "  background    : iperf3 @ ${BG_BITRATE}"
echo ""
echo "REMINDER: peer must run 'iperf3 -s'"
echo ""

# Start the background flow — wrap in a group so we can kill it cleanly
iperf3 -c "${PEER}" -t "$((DURATION+10))" -b "${BG_BITRATE}" -u >/dev/null 2>&1 &
BG_PID=$!
trap "kill ${BG_PID} 2>/dev/null || true" EXIT

# Let the bg flow ramp up for 2s
sleep 2

# Run the probe
"${PROBE_BIN}" -target "${PEER}:1" -cadence "${CADENCE_MS}ms" -duration "${DURATION}s" > "${OUT_FILE}" 2>&1 || true

kill "${BG_PID}" 2>/dev/null || true
wait "${BG_PID}" 2>/dev/null || true
trap - EXIT

# Prepend metadata
TMP_FILE="$(mktemp)"
{
    echo "# T3 TCP RTT under load via ${VPN}"
    echo "# Date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "# Peer VPN IP: ${PEER}"
    echo "# Probe cadence: ${CADENCE_MS} ms, duration: ${DURATION}s"
    echo "# Background: iperf3 UDP @ ${BG_BITRATE}"
    echo ""
    cat "${OUT_FILE}"
} > "${TMP_FILE}"
mv "${TMP_FILE}" "${OUT_FILE}"

echo ""
echo "Written to: ${OUT_FILE}"
