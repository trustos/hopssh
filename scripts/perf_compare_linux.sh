#!/usr/bin/env bash
# perf_compare_linux.sh — Head-to-head: hopssh vs Tailscale on LINUX.
#
# Mirrors scripts/perf_compare.sh but with roles swapped:
#   - Runs on the Mac mini (server side, this host)
#   - Linux VM is the iperf3 client, reached via plain LAN SSH (no Tailscale dep)
#   - Linux Tailscale CLI works fine over SSH (unlike macOS App Store CLI)
#
# Workloads per pass:
#   A. TCP single-stream 30s — captures cold-start ramp shape
#   B. Concurrent ping (per-100ms) — captures latency-under-load
#
# After 3 passes, runs once more (warm):
#   C. TCP 4-stream 30s — multi-flow aggregate (parallelism headroom)
#
# Output: results/perf-linux-YYYYMMDD-HHMMSS/
# Analyzer: scripts/perf_compare_analyze.sh handles the per-pass A/B json
# already; multi-stream gets parsed inline here.

set -uo pipefail

PASSES="${1:-3}"
TCP_DUR="${TCP_DUR:-30}"
MULTI_STREAMS="${MULTI_STREAMS:-4}"

HOPSSH_MINI_IP="${HOPSSH_MINI_IP:-10.42.1.7}"
HOPSSH_PORT="${HOPSSH_PORT:-5202}"
TS_MINI_IP="${TS_MINI_IP:-100.84.136.30}"
TS_PORT="${TS_PORT:-5203}"
LINUX_HOST="${LINUX_HOST:-trustos@192.168.23.232}"
LINUX_KEY="${LINUX_KEY:-/Users/tenevi/.ssh/id_ed25519}"
LAN_MINI_IP="${LAN_MINI_IP:-192.168.23.3}"
LAN_PORT="${LAN_PORT:-5201}"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TS_NOW="$(date +%Y%m%d-%H%M%S)"
OUTDIR="${REPO_ROOT}/results/perf-linux-${TS_NOW}"
mkdir -p "$OUTDIR"

log() { echo "[$(date +%H:%M:%S)] $*" | tee -a "$OUTDIR/run.log"; }

cleanup() {
  log "cleanup: stopping background iperf3 servers (if started by us)"
  for pid in ${IPERF_HOPSSH_PID:-} ${IPERF_TS_PID:-} ${IPERF_LAN_PID:-}; do
    [ -n "$pid" ] && kill "$pid" 2>/dev/null
  done
  return 0
}
trap cleanup EXIT

ssh_lx() {
  ssh -o ConnectTimeout=10 -o ServerAliveInterval=15 -i "$LINUX_KEY" "$LINUX_HOST" "$@"
}

# -------- iperf3 servers on mini --------------------------------------------

ensure_iperf3_server() {
  local bind_ip="$1" port="$2" label="$3"
  if pgrep -f "iperf3.*-s.*$bind_ip.*$port" >/dev/null 2>&1; then
    log "iperf3 server already up on $bind_ip:$port ($label)"
    return
  fi
  log "starting iperf3 server on $bind_ip:$port ($label)"
  iperf3 -s -B "$bind_ip" -p "$port" --daemon
  sleep 1
}

# -------- Pre-flight --------------------------------------------------------

check_linux_ready() {
  log "verifying Linux VM SSH"
  if ! ssh_lx "echo ok" >/dev/null 2>&1; then
    log "ERROR: Linux VM unreachable via SSH ($LINUX_HOST)"
    exit 1
  fi
  log "verifying Linux VM has iperf3, tailscale, hop-agent"
  if ! ssh_lx "command -v iperf3 && command -v tailscale && command -v hop-agent" >/dev/null 2>&1; then
    log "ERROR: missing iperf3/tailscale/hop-agent on Linux VM"
    exit 1
  fi
  log "verifying mesh reachability"
  if ! ssh_lx "ping -c2 -W2 $HOPSSH_MINI_IP" >/dev/null 2>&1; then
    log "ERROR: Linux VM cannot reach $HOPSSH_MINI_IP via hopssh"; exit 1
  fi
  if ! ssh_lx "ping -c2 -W2 $TS_MINI_IP" >/dev/null 2>&1; then
    log "ERROR: Linux VM cannot reach $TS_MINI_IP via Tailscale"; exit 1
  fi
}

snapshot_environment() {
  local f="$OUTDIR/environment.txt"
  {
    echo "=== Mini (server side) ==="
    echo "Host: $(hostname)"
    sw_vers 2>/dev/null
    /usr/local/bin/hop-agent --version 2>/dev/null
    echo
    echo "=== Linux VM (client side) ==="
    ssh_lx 'hostname; uname -a; lsb_release -a 2>/dev/null; hop-agent --version; tailscale version | head -1; iperf3 --version | head -1'
    echo
    echo "=== mini interfaces (mesh) ==="
    ifconfig | grep -B1 "10\.42\." | head -20
    echo
    echo "=== Linux interfaces ==="
    ssh_lx 'ip -br link | grep -E "tail|hop|nebula|enp"'
  } > "$f" 2>&1
  log "environment snapshot → environment.txt"
}

# -------- Path verification (logged per pass) -------------------------------

snapshot_path() {
  local label="$1" pass="$2"
  local f="$OUTDIR/pass${pass}-${label}-path.txt"
  {
    echo "=== Linux $label path verification ==="
    if [ "$label" = "hopssh" ]; then
      ssh_lx "sudo tail -n 30 /var/log/hop-agent.log 2>/dev/null || sudo journalctl -u hop-agent -n 30 --no-pager" \
        | grep -E "$HOPSSH_MINI_IP|10\.42\.1\.7|192\.168\.23\.3|relay|direct|Handshake|Tunnel" \
        | tail -20
    else
      ssh_lx "tailscale status | grep -E 'tenevis|$TS_MINI_IP'"
      echo "---"
      ssh_lx "tailscale ping -c 3 --until-direct=false $TS_MINI_IP 2>&1 | head -5"
    fi
  } > "$f" 2>&1
}

# -------- Cold-start teardown -----------------------------------------------

cold_start_hopssh() {
  log "  hopssh cold-start: systemctl restart hop-agent"
  ssh_lx "sudo systemctl restart hop-agent" >/dev/null 2>&1 || true
  sleep 8  # let agent re-init, lighthouse re-discover, peer endpoint propagate
  # Warm the tunnel so the first iperf3 packet doesn't pay handshake cost
  ssh_lx "ping -c 3 -W 2 $HOPSSH_MINI_IP" >/dev/null 2>&1 || true
}

cold_start_tailscale() {
  log "  Tailscale cold-start: down/up"
  ssh_lx "sudo tailscale down && sleep 2 && sudo tailscale up" >/dev/null 2>&1 || true
  sleep 5
  ssh_lx "ping -c 3 -W 2 $TS_MINI_IP" >/dev/null 2>&1 || true
}

# -------- One measurement (TCP single-stream + concurrent ping) -------------

measure_one() {
  local label="$1" ip="$2" port="$3" pass="$4"
  local prefix="$OUTDIR/pass${pass}-${label}"

  log "  ${label} measurement → ${ip}:${port} (pass ${pass})"

  snapshot_path "$label" "$pass"

  # Background ping (per-100ms) for RTT-under-load distribution
  ssh_lx "ping -c $((TCP_DUR * 10)) -i 0.1 $ip" \
    > "${prefix}-rtt.txt" 2>&1 &
  local PING_PID=$!

  # Background hop-agent log capture (only meaningful for hopssh)
  if [ "$label" = "hopssh" ]; then
    ssh_lx "sudo tail -n 0 -F /var/log/hop-agent.log 2>/dev/null || sudo journalctl -u hop-agent -f --no-pager" \
      > "${prefix}-agent.log" 2>&1 &
    local LOG_PID=$!
  fi

  # TCP single-stream: 30s, 1s intervals, json
  ssh_lx "iperf3 -c $ip -p $port -t $TCP_DUR -i 1 -P 1 --json" \
    > "${prefix}-iperf3-tcp.json" 2>&1
  local TCP_RC=$?

  kill "$PING_PID" 2>/dev/null || true
  [ -n "${LOG_PID:-}" ] && kill "$LOG_PID" 2>/dev/null
  wait 2>/dev/null || true

  if [ $TCP_RC -ne 0 ]; then
    log "  WARNING: ${label} pass ${pass} iperf3 TCP rc=$TCP_RC"
  else
    log "  ${label} pass ${pass}: complete"
  fi
}

# -------- Multi-stream (warm, no teardown) ----------------------------------

measure_multi() {
  local label="$1" ip="$2" port="$3"
  local prefix="$OUTDIR/passM-${label}"

  log "  ${label} multi-stream (-P $MULTI_STREAMS, ${TCP_DUR}s) → ${ip}:${port}"

  ssh_lx "iperf3 -c $ip -p $port -t $TCP_DUR -i 1 -P $MULTI_STREAMS --json" \
    > "${prefix}-iperf3-tcp-multi.json" 2>&1
  local RC=$?
  if [ $RC -ne 0 ]; then
    log "  WARNING: ${label} multi-stream rc=$RC"
  fi
}

# -------- Wire ceiling sanity check -----------------------------------------

measure_lan_ceiling() {
  log "raw LAN sanity check (no VPN, $LAN_MINI_IP:$LAN_PORT, 5s)"
  ssh_lx "iperf3 -c $LAN_MINI_IP -p $LAN_PORT -t 5 -i 1 --json" \
    > "$OUTDIR/lan-ceiling.json" 2>&1 || log "  (raw LAN failed — check that iperf3 server is bound on $LAN_MINI_IP:$LAN_PORT)"
}

# -------- Main run ----------------------------------------------------------

main() {
  log "perf_compare_linux.sh starting"
  log "OUTDIR: $OUTDIR"
  log "PASSES: $PASSES, TCP single ${TCP_DUR}s, multi -P $MULTI_STREAMS"

  ensure_iperf3_server "$HOPSSH_MINI_IP" "$HOPSSH_PORT" "hopssh"
  ensure_iperf3_server "$TS_MINI_IP"     "$TS_PORT"     "tailscale"
  ensure_iperf3_server "$LAN_MINI_IP"    "$LAN_PORT"    "lan-baseline"
  check_linux_ready
  snapshot_environment

  measure_lan_ceiling

  for ((pass=1; pass<=PASSES; pass++)); do
    log "===== Pass $pass of $PASSES ====="
    if (( pass % 2 == 1 )); then
      cold_start_hopssh
      measure_one "hopssh"    "$HOPSSH_MINI_IP" "$HOPSSH_PORT" "$pass"
      cold_start_tailscale
      measure_one "tailscale" "$TS_MINI_IP"     "$TS_PORT"     "$pass"
    else
      cold_start_tailscale
      measure_one "tailscale" "$TS_MINI_IP"     "$TS_PORT"     "$pass"
      cold_start_hopssh
      measure_one "hopssh"    "$HOPSSH_MINI_IP" "$HOPSSH_PORT" "$pass"
    fi
  done

  log "===== Multi-stream (warm) ====="
  measure_multi "hopssh"    "$HOPSSH_MINI_IP" "$HOPSSH_PORT"
  measure_multi "tailscale" "$TS_MINI_IP"     "$TS_PORT"

  log "===== All measurements complete ====="
  log "Run analyzer: bash scripts/perf_compare_analyze.sh $OUTDIR"
}

main
