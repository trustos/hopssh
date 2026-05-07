#!/usr/bin/env bash
# perf_compare_windows.sh — Head-to-head: hopssh vs Tailscale on WINDOWS.
#
# Mirrors scripts/perf_compare_linux.sh — same workload structure so the
# results are directly comparable to the Linux + macOS runs:
#   - Runs on the Mac mini (server side, this host)
#   - Windows VM is the iperf3 client over SSH (yavor@10.42.1.10)
#   - Service control via sc.exe (vs systemctl on Linux, launchctl on macOS)
#   - ZeroTier intentionally skipped — not installed on this Windows VM
#
# Workloads per pass:
#   A. TCP single-stream 30s — captures cold-start ramp shape
#   B. Concurrent ping (per-100ms) — captures latency-under-load
#
# Output: results/perf-windows-YYYYMMDD-HHMMSS/
set -uo pipefail

PASSES="${1:-3}"
TCP_DUR="${TCP_DUR:-30}"

WIN_USER="${WIN_USER:-yavor}"
WIN_HOPSSH_IP="${WIN_HOPSSH_IP:-10.42.1.10}"      # Windows on hopssh home network
WIN_TAILSCALE_IP="${WIN_TAILSCALE_IP:-100.120.37.109}"  # Windows on Tailscale

# Servers (mini side):
MINI_HOPSSH="${MINI_HOPSSH:-10.42.1.7}"
MINI_HOPSSH_PORT="${MINI_HOPSSH_PORT:-5202}"
MINI_TS="${MINI_TS:-100.84.136.30}"
MINI_TS_PORT="${MINI_TS_PORT:-5203}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TS=$(date +%Y%m%d-%H%M%S)
OUT="${ROOT}/results/perf-windows-${TS}"
mkdir -p "$OUT"
echo "==> Output: $OUT"

# Restart hop-agent on Windows for cold-start measurement.
restart_hopssh_win() {
    ssh -i ~/.ssh/id_ed25519 -o ConnectTimeout=5 "${WIN_USER}@${WIN_HOPSSH_IP}" \
        "sc.exe stop hop-agent && sc.exe start hop-agent" 2>&1 \
        | grep -v "WARNING:\|store now\|may need" | head -10
    sleep 8  # SCM start + first heartbeat + handshake to lighthouse
}

# Restart Tailscale on Windows. Tailscale daemon is service-managed;
# `tailscale.exe down/up` cycles the connection without restarting it.
# Note: `timeout /T` doesn't work over SSH (no console handle), so we
# use `ping 127.0.0.1 -n` as the sleep equivalent.
restart_ts_win() {
    ssh -i ~/.ssh/id_ed25519 -o ConnectTimeout=5 "${WIN_USER}@${WIN_HOPSSH_IP}" \
        'tailscale down & ping -n 3 127.0.0.1 >nul & tailscale up' 2>&1 \
        | grep -v "WARNING:\|store now\|may need" | head -5
    sleep 5
}

# Run iperf3 on Windows targeting the given server. iperf3 -J = JSON output.
# Always SSH via WIN_HOPSSH_IP (always reachable, matches the .e2e-connections
# admin path); the workload itself routes via whichever VPN's server IP we pass.
run_iperf3() {
    local label="$1"
    local pass="$2"
    local srv_ip="$3"
    local srv_port="$4"
    local out="$OUT/pass${pass}-${label}-iperf3-tcp.json"
    echo "    iperf3 ${label} -> ${srv_ip}:${srv_port}"
    ssh -i ~/.ssh/id_ed25519 -o ConnectTimeout=5 "${WIN_USER}@${WIN_HOPSSH_IP}" \
        "iperf3 -c ${srv_ip} -p ${srv_port} -t ${TCP_DUR} -i 1 -J" 2>/dev/null > "$out"
    # Headline number for live tail.
    if command -v jq >/dev/null; then
        peak=$(jq -r '[.intervals[].sum.bits_per_second] | max // 0' "$out" 2>/dev/null \
            | awk '{printf "%.1f", $1/1e6}')
        echo "      peak: ${peak} Mb/s"
    fi
}

# Concurrent ping during TCP load. Same SSH transport convention as iperf3.
run_ping() {
    local label="$1"
    local pass="$2"
    local target_ip="$3"
    local out="$OUT/pass${pass}-${label}-rtt.txt"
    ssh -i ~/.ssh/id_ed25519 -o ConnectTimeout=5 "${WIN_USER}@${WIN_HOPSSH_IP}" \
        "ping -n ${TCP_DUR} -w 1000 ${target_ip}" 2>/dev/null > "$out"
}

for ((pass=1; pass<=PASSES; pass++)); do
    echo "==> Pass ${pass}/${PASSES}"

    echo "  -- hopssh restart + workload --"
    restart_hopssh_win
    run_iperf3 "hopssh" "$pass" "$MINI_HOPSSH" "$MINI_HOPSSH_PORT" &
    IPF_PID=$!
    run_ping "hopssh" "$pass" "$MINI_HOPSSH"
    wait "$IPF_PID" 2>/dev/null || true

    echo "  -- tailscale restart + workload --"
    restart_ts_win
    run_iperf3 "tailscale" "$pass" "$MINI_TS" "$MINI_TS_PORT" &
    IPF_PID=$!
    run_ping "tailscale" "$pass" "$MINI_TS"
    wait "$IPF_PID" 2>/dev/null || true
done

echo "==> Done. Output: $OUT"
echo "==> Run analyzer: scripts/perf_compare_analyze.sh $OUT"
