#!/usr/bin/env bash
# cellular_idle_test.sh — End-to-end verification that Phase G
# (HTTPS-distributed self-endpoints, shipped v0.10.22) prevents the
# 2026-04-25 production incident from recurring.
#
# THE INCIDENT:
#   MBP on iPhone Personal Hotspot established mesh to mini-home.
#   After ~16 min of mesh idle:
#     - MBP cellular CGNAT closed the idle UDP flow to mini
#     - MBP's UDP-to-lighthouse path is carrier-filtered, so the
#       lighthouse never re-learned MBP's current advertise_addrs
#     - The control plane's view of MBP's endpoint went stale
#     - Mini's heartbeat response stopped including MBP's endpoint
#     - Mini's hostmap still had the (now wrong) port from 16 min ago
#     - First packet either direction failed; mesh appeared "frozen"
#       until manual `hop-agent restart` on MBP
#
# THE FIX (Phase G):
#   Agent's HTTPS heartbeat now carries `selfEndpoints` — the agent's
#   own observed reachable endpoints (NAT-PMP public + local interface
#   IPs paired with listen port). Server caches per-node with 15-min
#   TTL and merges them into peerEndpoints responses to OTHER agents.
#   Independent of UDP-to-lighthouse — works even when the carrier
#   blocks lighthouse UDP entirely.
#
# WHAT THIS TEST PROVES:
#   1. Throughout a 15+ min idle window, mini's hop-agent log keeps
#      receiving fresh `lighthouse: added static host map entry` for
#      MBP's mesh IP (= Phase G actively flowing data via heartbeat,
#      independent of UDP-to-lighthouse)
#   2. After the idle window, the very first packet between the two
#      peers succeeds (no manual restart, no multi-second handshake
#      timeout) — = the mesh actually recovers as intended
#
# CAVEAT:
#   Phase B-lite's path_quality goroutine (v0.10.21+) sends one
#   TCP-SYN to each direct peer every 10s, which incidentally keeps
#   the cellular CGNAT outbound flow warm. Even without Phase G, this
#   probe traffic alone might prevent the carrier-side flow from
#   timing out in some scenarios. The test still validates end-to-end
#   recovery; if you want to ALSO isolate Phase G's contribution
#   independent of B-lite, set HOPSSH_DISABLE_PATH_QUALITY=1 (when
#   that env var is honored — currently always-on in v0.10.21+).
#
# USAGE:
#   bash scripts/cellular_idle_test.sh [IDLE_MINUTES]
#   IDLE_MINUTES defaults to 17 (one full carrier-CGNAT timeout +
#   buffer). Total runtime is ~ IDLE_MINUTES + 2 min for setup +
#   verification.
#
# PRE-FLIGHT:
#   - MBP on cellular (NOT on the same WiFi as mini)
#   - Both agents v0.10.22+
#   - SSH to MBP works via a SECONDARY path (Tailscale, etc.) so we
#     don't generate hopssh-mesh traffic to MBP during the idle window
#   - Mini's mesh IP, MBP's mesh IP known
#
# EXIT CODES: 0 PASS, 1 FAIL, 2 PRE-FLIGHT FAILURE.

set -uo pipefail

IDLE_MINUTES="${1:-17}"
MINI_MESH_IP="${MINI_MESH_IP:-10.42.1.7}"
MBP_MESH_IP="${MBP_MESH_IP:-10.42.1.11}"
MBP_SSH="${MBP_SSH:-yavortenev@100.107.90.106}"   # Tailscale, NOT mesh
MINI_LOG="${MINI_LOG:-/var/log/hop-agent.log}"
SUDO="${SUDO:-sudo}"
MIN_HEARTBEAT_PUSHES="${MIN_HEARTBEAT_PUSHES:-2}"  # at least N pushes during idle window

OUTDIR="${OUTDIR:-results/cellular-idle-$(date +%Y%m%d-%H%M%S)}"
mkdir -p "$OUTDIR"
LOG="$OUTDIR/run.log"

log() { echo "[$(date +%H:%M:%S)] $*" | tee -a "$LOG"; }

require() {
  if ! eval "$1" >/dev/null 2>&1; then
    log "PRE-FLIGHT FAIL: $2"
    exit 2
  fi
}

ssh_mbp() {
  ssh -o ConnectTimeout=10 -o ServerAliveInterval=15 "$MBP_SSH" "$@"
}

# -------- Pre-flight ------------------------------------------------------

log "==== Phase G end-to-end verification ===="
log "Output: $OUTDIR"
log "Idle window: ${IDLE_MINUTES} minutes"
log "Mini mesh IP: $MINI_MESH_IP   MBP mesh IP: $MBP_MESH_IP"
log "MBP SSH (out-of-mesh): $MBP_SSH"
log ""
log "Pre-flight checks..."

require "ssh_mbp echo ok" "cannot reach MBP via $MBP_SSH (Tailscale or LAN)"
require "$SUDO test -r $MINI_LOG" "mini's $MINI_LOG not readable (run as user with sudo)"

mini_ver=$(/usr/local/bin/hop-agent --version 2>&1 | head -1)
mbp_ver=$(ssh_mbp /usr/local/bin/hop-agent --version 2>&1 | head -1)
log "  mini agent: $mini_ver"
log "  MBP  agent: $mbp_ver"

# Confirm both at v0.10.22+ (Phase G ships there).
for v in "$mini_ver" "$mbp_ver"; do
  if ! echo "$v" | grep -qE 'v0\.(1[0-9]\.(2[2-9]|[3-9][0-9])|[2-9][0-9]\.|[0-9]+\.[0-9]+)'; then
    log "PRE-FLIGHT FAIL: agent version is below v0.10.22 (no Phase G): $v"
    exit 2
  fi
done

# Confirm baseline mesh works.
log ""
log "Baseline ping check..."
if ! ssh_mbp "ping -c 2 -W 2 $MINI_MESH_IP" >"$OUTDIR/baseline-mbp-ping.txt" 2>&1; then
  log "PRE-FLIGHT FAIL: MBP cannot ping mini at $MINI_MESH_IP — mesh not up"
  cat "$OUTDIR/baseline-mbp-ping.txt" | tail -5 | tee -a "$LOG"
  exit 2
fi
log "  MBP -> mini OK"
if ! ping -c 2 -W 2 "$MBP_MESH_IP" >"$OUTDIR/baseline-mini-ping.txt" 2>&1; then
  log "PRE-FLIGHT FAIL: mini cannot ping MBP at $MBP_MESH_IP — mesh not up"
  cat "$OUTDIR/baseline-mini-ping.txt" | tail -5 | tee -a "$LOG"
  exit 2
fi
log "  mini -> MBP OK"

# -------- Capture log baseline ------------------------------------------

start_offset=$($SUDO wc -c < "$MINI_LOG")
log ""
log "Mini log baseline byte offset: $start_offset"
log "(All Phase G evidence will be measured from new log lines after this offset.)"

# -------- Idle window with periodic snapshots ----------------------------

idle_seconds=$((IDLE_MINUTES * 60))
log ""
log "==== Beginning $IDLE_MINUTES-minute idle window at $(date +%H:%M:%S) ===="
log "DO NOT generate traffic between the two peers during this window."
log "(SSH-via-Tailscale to MBP is fine — that's a separate path.)"

snapshot_interval=300  # 5 minutes
elapsed=0
push_count=0
while [ $elapsed -lt $idle_seconds ]; do
  remaining=$((idle_seconds - elapsed))
  to_sleep=$((snapshot_interval < remaining ? snapshot_interval : remaining))
  sleep $to_sleep
  elapsed=$((elapsed + to_sleep))

  cur_offset=$($SUDO wc -c < "$MINI_LOG")
  delta=$((cur_offset - start_offset))
  pushes=$($SUDO tail -c +"$((start_offset + 1))" "$MINI_LOG" 2>/dev/null \
           | grep -cE "added static host map entry.*vpnAddr=$MBP_MESH_IP" || echo 0)
  log "[+$((elapsed/60))m] Mini log grew ${delta} bytes; saw $pushes Phase-G push(es) for $MBP_MESH_IP"
  push_count=$pushes
done

log ""
log "==== Idle window complete at $(date +%H:%M:%S) ===="

# -------- Post-idle ping (the moment of truth) ---------------------------

log ""
log "Post-idle connectivity check (= recovery from cellular CGNAT-evict)..."

t_start=$(date +%s.%N)
mbp_ping_rc=0
ssh_mbp "ping -c 5 -W 3 $MINI_MESH_IP" >"$OUTDIR/postidle-mbp-ping.txt" 2>&1 || mbp_ping_rc=$?
mbp_ping_dur=$(awk "BEGIN{printf \"%.1f\", $(date +%s.%N) - $t_start}")
log "  MBP -> mini: rc=$mbp_ping_rc, dur=${mbp_ping_dur}s"
tail -3 "$OUTDIR/postidle-mbp-ping.txt" | sed 's/^/    /' | tee -a "$LOG"

t_start=$(date +%s.%N)
mini_ping_rc=0
ping -c 5 -W 3 "$MBP_MESH_IP" >"$OUTDIR/postidle-mini-ping.txt" 2>&1 || mini_ping_rc=$?
mini_ping_dur=$(awk "BEGIN{printf \"%.1f\", $(date +%s.%N) - $t_start}")
log "  mini -> MBP: rc=$mini_ping_rc, dur=${mini_ping_dur}s"
tail -3 "$OUTDIR/postidle-mini-ping.txt" | sed 's/^/    /' | tee -a "$LOG"

# -------- Pass/fail report ----------------------------------------------

log ""
log "==== Pass/Fail Report ===="

phase_g_pass=true
recovery_pass=true

if [ "$push_count" -lt "$MIN_HEARTBEAT_PUSHES" ]; then
  log "FAIL: only $push_count Phase-G pushes for $MBP_MESH_IP (want >= $MIN_HEARTBEAT_PUSHES)."
  log "      Expected one push per ~5min heartbeat → at least 2 in $IDLE_MINUTES min."
  log "      Phase G is not flowing data through the heartbeat — REGRESSION SUSPECTED."
  phase_g_pass=false
else
  log "PASS: saw $push_count Phase-G heartbeat-driven endpoint pushes for MBP during idle"
  log "      (Server is actively distributing MBP's selfEndpoints despite zero mesh traffic)"
fi

if [ $mbp_ping_rc -ne 0 ] || [ $mini_ping_rc -ne 0 ]; then
  log "FAIL: post-idle ping did NOT recover (mbp_rc=$mbp_ping_rc, mini_rc=$mini_ping_rc)"
  log "      The cellular-idle silent-mesh bug HAS RECURRED."
  recovery_pass=false
else
  log "PASS: both directions ping after $IDLE_MINUTES min idle (mbp=${mbp_ping_dur}s, mini=${mini_ping_dur}s)"
  log "      Mesh recovered without manual restart — Phase G is doing its job."
fi

log ""
if $phase_g_pass && $recovery_pass; then
  log "==== OVERALL: PASS — Phase G prevents the 2026-04-25 incident ===="
  exit 0
else
  log "==== OVERALL: FAIL — see details above ===="
  log "Saved evidence in: $OUTDIR"
  exit 1
fi
