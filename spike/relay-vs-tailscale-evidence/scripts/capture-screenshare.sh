#!/usr/bin/env bash
# T5: Capture a real Screen Sharing session over a VPN tunnel.
#
# Opens macOS Screen Sharing to <peer-vpn-ip>, runs tcpdump on the VPN's utun
# interface for <duration> seconds, saves the pcap.
#
# The user must drive the visual workload manually during the capture window
# (drag a window, run a counter animation, etc.) — this script only starts the
# session and captures the wire trace.
#
# Usage: sudo ./capture-screenshare.sh <vpn-name> <peer-vpn-ip> [duration-seconds]
#
# vpn-name: hopssh | tailscale (used for labelling output files)

set -euo pipefail

if [[ $# -lt 2 ]]; then
    echo "usage: sudo $0 <vpn-name> <peer-vpn-ip> [duration-seconds]" >&2
    echo "" >&2
    echo "example: sudo $0 hopssh 10.42.1.7 60" >&2
    exit 1
fi

if [[ $EUID -ne 0 ]]; then
    echo "must be run as root (tcpdump needs raw socket access)" >&2
    exit 1
fi

VPN="$1"
PEER="$2"
DURATION="${3:-60}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT_DIR="$(dirname "${SCRIPT_DIR}")"
PCAP="${OUT_DIR}/05-${VPN}.pcap"

# Determine the VPN's utun interface by finding the utun that has the
# /24 containing the peer IP bound to it.
PEER_PREFIX=$(echo "${PEER}" | awk -F. '{printf "%s.%s.%s.", $1, $2, $3}')
VPN_IFACE=$(ifconfig | awk -v p="${PEER_PREFIX}" '
    /^utun[0-9]+/ { iface=$1; sub(":$","",iface) }
    /^\tinet / && $2 ~ p { print iface; exit }
')

if [[ -z "${VPN_IFACE}" ]]; then
    echo "could not find VPN utun interface for peer ${PEER}" >&2
    echo "available interfaces:" >&2
    ifconfig | grep -E '^(utun|en)[0-9]+' | awk '{print "  "$1}' >&2
    exit 1
fi

echo "Capturing ${VPN} screen-share session"
echo "  interface : ${VPN_IFACE}"
echo "  peer      : ${PEER}"
echo "  duration  : ${DURATION}s"
echo "  pcap out  : ${PCAP}"
echo ""

# Start tcpdump in background
tcpdump -i "${VPN_IFACE}" -w "${PCAP}" -s 200 "host ${PEER}" &
TCPDUMP_PID=$!
trap "kill ${TCPDUMP_PID} 2>/dev/null || true" EXIT

sleep 1

# Start Screen Sharing via URL scheme (opens the macOS Screen Sharing app)
echo ">>> Screen Sharing window will open. Authenticate, then drive your workload."
echo ">>> Suggested: drag a window in circles, or open a page with a running clock."
echo ""
open "vnc://${PEER}"

# Wait for capture window to close
for ((i=DURATION; i>0; i--)); do
    printf "\rcapturing... %3ds remaining " "$i"
    sleep 1
done
printf "\n"

kill "${TCPDUMP_PID}" 2>/dev/null || true
wait "${TCPDUMP_PID}" 2>/dev/null || true
trap - EXIT

# Quick stats
echo ""
echo "Packets captured:"
tcpdump -r "${PCAP}" 2>/dev/null | wc -l | xargs echo "  total:"
echo ""
echo "Per-second byte rate (first 10s):"
tshark -r "${PCAP}" -q -z io,stat,1 2>/dev/null | head -20 || echo "  (install wireshark for io stats)"
echo ""
echo "Saved: ${PCAP}"
echo ""
echo "TCP RTT distribution: tshark -r ${PCAP} -q -z tcp,stat"
echo "IO stats:             tshark -r ${PCAP} -q -z io,stat,1"
