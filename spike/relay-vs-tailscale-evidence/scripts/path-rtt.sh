#!/usr/bin/env bash
# T1: Raw path RTT to the hopssh relay and the nearest Tailscale DERP.
#
# Usage: ./path-rtt.sh [hopssh-relay-host]
#
# If hopssh-relay-host is not given, reads from the agent enrollment config.
# Writes 01-path-rtt.log in the parent directory.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT_DIR="$(dirname "${SCRIPT_DIR}")"
OUT_FILE="${OUT_DIR}/01-path-rtt.log"
PING_COUNT=60

# Resolve hopssh relay host
HOPSSH_RELAY="${1:-}"
if [[ -z "${HOPSSH_RELAY}" ]]; then
    # Try to read from the first enrollment's nebula.yaml. Config dir is
    # /etc/hop-agent on Linux / darwin with system install; user-level falls
    # back to ~/Library/Application Support/hopssh (macOS) or ~/.config/hopssh.
    for CFG_DIR in /etc/hop-agent "${HOME}/Library/Application Support/hopssh" "${HOME}/.config/hopssh"; do
        NEBULA_CFG="$(ls -1 "${CFG_DIR}"/*/nebula.yaml 2>/dev/null | head -n1 || true)"
        if [[ -n "${NEBULA_CFG}" ]]; then break; fi
    done
    if [[ -n "${NEBULA_CFG:-}" ]]; then
        # Extract the lighthouse public hostname from static_host_map entry
        HOPSSH_RELAY=$(awk -F'"' '/static_host_map:/{inmap=1;next} inmap && /^  "/{print $4; exit}' "${NEBULA_CFG}" | cut -d: -f1)
    fi
fi
if [[ -z "${HOPSSH_RELAY}" ]]; then
    echo "could not determine hopssh relay host. Pass it as arg 1." >&2
    exit 1
fi

# Find Tailscale's preferred DERP
if ! command -v tailscale >/dev/null; then
    echo "tailscale CLI not found; install or switch to it first" >&2
    exit 1
fi

DERP_REGION=$(tailscale netcheck --format=json 2>/dev/null | python3 -c "
import json,sys
d=json.load(sys.stdin)
# Preferred DERP region is the one with the lowest RTT
regions=d.get('RegionLatency',{})
if not regions:
    print('',end='')
else:
    best=min(regions.items(),key=lambda kv:kv[1])
    print(best[0])
" 2>/dev/null || true)

if [[ -z "${DERP_REGION}" ]]; then
    # Fallback: parse text output
    DERP_REGION=$(tailscale netcheck 2>&1 | awk '/DERP latency:/{getline; print $1; exit}' | tr -d ':' || true)
fi

# Map region name to hostname — Tailscale DERPs use the pattern derp<N>.tailscale.com
# but the netcheck output gives friendly names (nyc, fra, ams, etc.).
# We'll just extract the preferred DERP's host from the map.
DERP_HOST=$(tailscale debug derp-map 2>/dev/null | python3 -c "
import json,sys,re
d=json.load(sys.stdin)
region_name='${DERP_REGION}'
for rid, r in d.get('Regions',{}).items():
    if r.get('RegionCode')==region_name or r.get('RegionName','').lower().startswith(region_name.lower()):
        nodes=r.get('Nodes',[])
        if nodes:
            print(nodes[0].get('HostName',''))
            break
" 2>/dev/null || true)

if [[ -z "${DERP_HOST}" ]]; then
    echo "could not resolve Tailscale DERP host; falling back to derp1.tailscale.com" >&2
    DERP_HOST="derp1.tailscale.com"
fi

echo "hopssh relay : ${HOPSSH_RELAY}"
echo "Tailscale DERP: ${DERP_HOST} (region: ${DERP_REGION:-unknown})"
echo "Pinging each for ${PING_COUNT} packets..."
echo ""

{
    echo "# T1 Path RTT (raw, no VPN)"
    echo "# Date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "# hopssh relay : ${HOPSSH_RELAY}"
    echo "# Tailscale DERP: ${DERP_HOST} (region: ${DERP_REGION:-unknown})"
    echo "# Ping count: ${PING_COUNT}"
    echo ""
    echo "## hopssh relay"
    ping -c "${PING_COUNT}" -i 1 "${HOPSSH_RELAY}" 2>&1 | tail -n 4
    echo ""
    echo "## Tailscale DERP"
    ping -c "${PING_COUNT}" -i 1 "${DERP_HOST}" 2>&1 | tail -n 4
} | tee "${OUT_FILE}"

echo ""
echo "Written to: ${OUT_FILE}"
