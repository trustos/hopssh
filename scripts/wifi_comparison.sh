#!/usr/bin/env bash
# wifi_comparison.sh — Three-VPN sustained ping comparison + Tailscale path
# discrimination + MTU bisection. Runs from Mac mini against MBP.
#
# Usage:
#   scripts/wifi_comparison.sh test1 [duration_hours]   # default 24
#   scripts/wifi_comparison.sh test2                    # MTU bisection (3h)
#   scripts/wifi_comparison.sh test3                    # Tailscale P2P/DERP (1h)
#   scripts/wifi_comparison.sh all                      # 1+2+3 sequential
#
# Output: results/wifi-comparison-YYYYMMDD-HHMMSS/
set -euo pipefail

# ===== Targets (override via env) =====
HOPSSH_TARGET="${HOPSSH_TARGET:-10.42.1.11}"          # MBP home enrollment
TAILSCALE_TARGET="${TAILSCALE_TARGET:-100.107.90.106}" # MBP Tailscale
ZEROTIER_TARGET="${ZEROTIER_TARGET:-10.147.18.193}"    # MBP ZT Home Network

# ===== Constants (override via env for smoke-tests) =====
WINDOW_SEC="${WINDOW_SEC:-300}"          # Test 1: 5-min rotation per VPN
PING_INTERVAL="${PING_INTERVAL:-1}"      # 1 Hz
WIFI_SAMPLE_SEC="${WIFI_SAMPLE_SEC:-60}" # WiFi state cadence

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TS=$(date +%Y%m%d-%H%M%S)
OUT="${ROOT}/results/wifi-comparison-${TS}"

# ===== Pre-flight =====
preflight() {
    local missing=()
    command -v ping >/dev/null || missing+=("ping")
    [[ -n "$HOPSSH_TARGET"  ]] && ping -c1 -W2000 -t1 "$HOPSSH_TARGET"  >/dev/null 2>&1 || \
        echo "WARN: hopssh target $HOPSSH_TARGET unreachable"
    [[ -n "$TAILSCALE_TARGET" ]] && ping -c1 -W2000 -t1 "$TAILSCALE_TARGET" >/dev/null 2>&1 || \
        echo "WARN: tailscale target $TAILSCALE_TARGET unreachable"
    [[ -n "$ZEROTIER_TARGET" ]] && ping -c1 -W2000 -t1 "$ZEROTIER_TARGET" >/dev/null 2>&1 || \
        echo "WARN: zerotier target $ZEROTIER_TARGET unreachable"

    command -v tailscale >/dev/null || echo "WARN: tailscale CLI not found — Test 3 will be skipped"
    command -v wdutil >/dev/null    || echo "WARN: wdutil not found — WiFi state sampling skipped"

    mkdir -p "$OUT"
    echo "==> Output: $OUT"
}

# ===== WiFi state sampler (caller backgrounds) =====
# Caller must invoke as `wifi_sampler "$out" >/dev/null 2>&1 &`
# (the function itself writes to file; we redirect stdio so $(...) doesn't block)
wifi_sampler() {
    local out="$1"
    printf '%s\t%s\n' "ts" "wifi_summary" > "$out"
    while sleep "$WIFI_SAMPLE_SEC"; do
        ts=$(date +%s)
        # wdutil prints multi-line; flatten to a single line of key=val pairs.
        summary=$(sudo wdutil info 2>/dev/null \
            | awk '
                /^WIFI/{section="wifi"; next}
                /^[A-Z][A-Z]+/{section=""}
                section=="wifi" && /:/{
                    gsub(/^[[:space:]]+/,"")
                    gsub(/[[:space:]]+/," ")
                    gsub(/\t/," ")
                    gsub(/ +/," ")
                    if (NR<200) printf "%s; ", $0
                }
            ' | head -c 2000)
        printf '%s\t%s\n' "$ts" "$summary" >> "$out"
    done
}

# ===== Ping a VPN target for N seconds, label rows =====
ping_window() {
    local label="$1"
    local target="$2"
    local seconds="$3"
    local out="$4"
    local end=$(( $(date +%s) + seconds ))
    # Use macOS ping; -i 1.0 = 1Hz; -W 2000 = 2s timeout per probe.
    while (( $(date +%s) < end )); do
        # One ping at a time so we can timestamp + label every line.
        out_line=$(ping -c1 -W2000 -i 1.0 -t 64 "$target" 2>/dev/null \
            | awk '/time=/{ for(i=1;i<=NF;i++) if($i~/time=/){ sub("time=","",$i); print $i; exit } }')
        ts=$(date +%s.%N)
        if [[ -n "$out_line" ]]; then
            printf '%s\t%s\t%s\n' "$ts" "$label" "$out_line" >> "$out"
        else
            printf '%s\t%s\tLOSS\n' "$ts" "$label" >> "$out"
        fi
        # Pace at ~1Hz (ping -c1 returns immediately on success or after timeout)
        sleep 0.5 || true
    done
}

# ===== Test 1: rotate VPNs every 5 min for N hours =====
test1() {
    local hours="${1:-24}"
    local pings_file="$OUT/pings.tsv"
    local wifi_file="$OUT/wifi-state.tsv"

    echo "==> Test 1: $hours h sustained 3-VPN ping rotation (5-min windows)"
    printf '%s\t%s\t%s\n' "ts" "vpn" "rtt_ms_or_LOSS" > "$pings_file"

    wifi_sampler "$wifi_file" </dev/null >/dev/null 2>&1 &
    local sampler_pid=$!
    trap "kill $sampler_pid 2>/dev/null || true" EXIT

    local end=$(( $(date +%s) + hours * 3600 ))
    local rotation=("hopssh:$HOPSSH_TARGET" "tailscale:$TAILSCALE_TARGET" "zerotier:$ZEROTIER_TARGET")
    local idx=0
    while (( $(date +%s) < end )); do
        IFS=':' read -r label target <<< "${rotation[$idx]}"
        echo "    [$(date +%H:%M:%S)] window: $label ($target)"
        ping_window "$label" "$target" "$WINDOW_SEC" "$pings_file" || true
        idx=$(( (idx + 1) % ${#rotation[@]} ))
    done
    echo "==> Test 1 done. Output: $pings_file"
}

# ===== Test 2: MTU bisection (3 windows × 1 hour) =====
test2() {
    local pings_file="$OUT/pings-mtu.tsv"
    echo "==> Test 2: MTU bisection (1h × 3 = 3h)"
    printf '%s\t%s\t%s\n' "ts" "label" "rtt_ms_or_LOSS" > "$pings_file"

    echo "==> Set hopssh MTU=1420 (production default)"
    "$ROOT/scripts/set-mtu.sh" 1420
    sleep 30  # allow handshake to settle on the new MTU
    ping_window "hopssh-mtu1420" "$HOPSSH_TARGET" 3600 "$pings_file"

    echo "==> Set hopssh MTU=1280 (Tailscale-equivalent)"
    "$ROOT/scripts/set-mtu.sh" 1280
    sleep 30
    ping_window "hopssh-mtu1280" "$HOPSSH_TARGET" 3600 "$pings_file"

    echo "==> Tailscale (own MTU 1280)"
    ping_window "tailscale" "$TAILSCALE_TARGET" 3600 "$pings_file"

    echo "==> Restore hopssh MTU=1420"
    "$ROOT/scripts/set-mtu.sh" 1420
    echo "==> Test 2 done. Output: $pings_file"
}

# ===== Test 3: Tailscale P2P-vs-DERP discrimination over 1h =====
test3() {
    local status_file="$OUT/tailscale-status-snapshots.jsonl"
    local netcheck_file="$OUT/tailscale-netcheck.txt"
    local pings_file="$OUT/pings-tailscale.tsv"
    echo "==> Test 3: 1h Tailscale path discrimination"
    printf '%s\t%s\t%s\n' "ts" "label" "rtt_ms_or_LOSS" > "$pings_file"
    : > "$status_file"

    # Background: poll tailscale status every 30s
    (
        local end=$(( $(date +%s) + 3600 ))
        while (( $(date +%s) < end )); do
            ts=$(date +%s)
            snap=$(tailscale status --json 2>/dev/null || echo '{}')
            jq -c --arg ts "$ts" '. + {ts: ($ts|tonumber)}' <<< "$snap" >> "$status_file" 2>/dev/null \
                || echo "{\"ts\":$ts,\"raw\":$(jq -Rs . <<< "$snap")}" >> "$status_file"
            sleep 30
        done
    ) &
    local poll_pid=$!

    # Background: tailscale netcheck every 5 min
    (
        local end=$(( $(date +%s) + 3600 ))
        while (( $(date +%s) < end )); do
            ts=$(date +%s)
            { echo "=== ts=$ts ==="; tailscale netcheck 2>&1; } >> "$netcheck_file"
            sleep 300
        done
    ) &
    local netcheck_pid=$!

    trap "kill $poll_pid $netcheck_pid 2>/dev/null || true" RETURN
    ping_window "tailscale" "$TAILSCALE_TARGET" 3600 "$pings_file"
    wait "$poll_pid" 2>/dev/null || true
    wait "$netcheck_pid" 2>/dev/null || true
    echo "==> Test 3 done. Output: $status_file, $netcheck_file, $pings_file"
}

# ===== Test 4: Active iperf3 UDP workload + concurrent ping =====
# Models VNC/RTP-style sustained throughput. iperf3 server expected on
# MBP at port 5300. Runs 30 min per VPN. Captures ping under load.
test4() {
    local pings_file="$OUT/pings-load.tsv"
    local iperf_log="$OUT/iperf3-load.log"
    local rate="${WORKLOAD_RATE:-10M}"   # ~VNC peak; override e.g. WORKLOAD_RATE=20M
    local len="${WORKLOAD_LEN:-1300}"    # MTU-safe RTP-ish payload
    local secs="${WORKLOAD_WINDOW:-1800}" # 30 min
    echo "==> Test 4: 30-min active workload (UDP $rate, $len-byte) + concurrent ping per VPN"
    printf '%s\t%s\t%s\n' "ts" "label" "rtt_ms_or_LOSS" > "$pings_file"
    : > "$iperf_log"

    if ! command -v iperf3 >/dev/null; then
        echo "ERROR: iperf3 not found locally. brew install iperf3" >&2
        return 2
    fi
    # Bring up iperf3 server on MBP if not already.
    ssh "${LAPTOP_USER:-yavortenev}@${LAPTOP_HOST:-192.168.23.18}" \
        'pgrep -f "iperf3 -s -p 5300" >/dev/null || nohup iperf3 -s -p 5300 >/dev/null 2>&1 &' \
        2>/dev/null || true
    sleep 2

    for entry in "hopssh:$HOPSSH_TARGET" "tailscale:$TAILSCALE_TARGET" "zerotier:$ZEROTIER_TARGET"; do
        IFS=':' read -r label target <<< "$entry"
        echo "    [$(date +%H:%M:%S)] $secs s under load via $label ($target)"
        # Background iperf3 sustained UDP stream. Output discarded; -t bounds runtime.
        iperf3 -c "$target" -p 5300 -u -b "$rate" -l "$len" -t "$secs" \
            >> "$iperf_log" 2>&1 &
        local iperf_pid=$!
        # Concurrently ping the same target.
        ping_window "$label" "$target" "$secs" "$pings_file" &
        local ping_pid=$!
        wait "$iperf_pid" 2>/dev/null || true
        wait "$ping_pid" 2>/dev/null || true
        # Brief idle gap between VPNs to let buffers drain.
        sleep 30
    done
    echo "==> Test 4 done. Output: $pings_file, $iperf_log"
}

# ===== Test 5: WiFi pcap on pktap during hopssh + Tailscale windows =====
# pktap surfaces 802.11 metadata when available on Apple Silicon. Pure
# tcpdump capture; analysis (retry/MCS distribution) is post-hoc.
test5() {
    local secs="${PCAP_WINDOW:-1800}"   # 30 min per VPN
    local iface="${PCAP_IFACE:-en1}"    # WiFi physical
    echo "==> Test 5: pktap capture on $iface, $secs s per VPN (sudo required)"
    if ! sudo -n true 2>/dev/null; then
        echo "ERROR: sudo cache required. Run 'sudo -v' first." >&2
        return 2
    fi
    for entry in "hopssh:$HOPSSH_TARGET" "tailscale:$TAILSCALE_TARGET"; do
        IFS=':' read -r label target <<< "$entry"
        local pcap="$OUT/pcap-$label.pcap.gz"
        local pings_file="$OUT/pings-pcap.tsv"
        [[ -f "$pings_file" ]] || printf '%s\t%s\t%s\n' "ts" "label" "rtt_ms_or_LOSS" > "$pings_file"
        echo "    [$(date +%H:%M:%S)] $secs s pcap + ping via $label ($target)"
        sudo tcpdump -i pktap -y PPI -G "$secs" -W 1 -w - "host $target" 2>/dev/null \
            | gzip > "$pcap" &
        local cap_pid=$!
        ping_window "$label" "$target" "$secs" "$pings_file" || true
        wait "$cap_pid" 2>/dev/null || true
    done
    echo "==> Test 5 done. Output: $OUT/pcap-*.pcap.gz, $OUT/pings-pcap.tsv"
}

# ===== Dispatch =====
cmd="${1:-}"
case "$cmd" in
    test1|test2|test3|test4|test5|all) ;;
    "" ) echo "usage: $0 {test1 [hours]|test2|test3|test4|test5|all}" >&2; exit 2 ;;
    * )  echo "unknown command: $cmd" >&2; exit 2 ;;
esac

preflight

case "$cmd" in
    test1) test1 "${2:-24}" ;;
    test2) test2 ;;
    test3) test3 ;;
    test4) test4 ;;
    test5) test5 ;;
    all)   test1 24; test2; test3; test4; test5 ;;
esac

cat > "$OUT/README.md" <<EOF
# WiFi comparison results — $TS

## Targets
- hopssh:    \`$HOPSSH_TARGET\` (MBP home enrollment)
- tailscale: \`$TAILSCALE_TARGET\` (MBP Tailscale)
- zerotier:  \`$ZEROTIER_TARGET\` (MBP ZT Home Network)

## Files
- \`pings.tsv\` — Test 1: rotated 5-min windows, 1 Hz ping
- \`pings-mtu.tsv\` — Test 2: hopssh@1420 vs hopssh@1280 vs Tailscale
- \`pings-tailscale.tsv\` — Test 3: ping during P2P/DERP discrimination
- \`tailscale-status-snapshots.jsonl\` — Test 3: status polled every 30s
- \`tailscale-netcheck.txt\` — Test 3: netcheck every 5 min
- \`wifi-state.tsv\` — wdutil snapshots every 60s

## Analysis
Run \`scripts/analyze_wifi_comparison.py $OUT\` for the verdict.
EOF
echo "==> Wrote $OUT/README.md"
echo "==> Run: scripts/analyze_wifi_comparison.py $OUT"
