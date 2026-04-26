#!/usr/bin/env bash
# test_keepalive_recovery.sh — Real-world integration test for the
# v0.10.31 stale-keep-alive fix. Runs on a Linux host with hop-agent
# installed. Simulates the post-network-change scenario by black-holing
# the route to the control plane for a few seconds, then measures how
# long it takes for the next heartbeat to succeed after the route is
# restored.
#
# Usage:
#   sudo scripts/test_keepalive_recovery.sh [block_seconds]
#
# Pre-fix (v0.10.30 and earlier): expect 60-180s recovery while Go's
# DefaultTransport ages out the stale idle conns one at a time.
# Post-fix (v0.10.31+): expect ≤30s recovery — the next heartbeat after
# unblock dials a fresh connection and succeeds immediately.
#
# Decisive criterion:
#   first POST-block heartbeat success ≤ 30s = FIX IS WORKING
#   first POST-block heartbeat success  > 60s = REGRESSION (or wrong binary)
set -euo pipefail

BLOCK_SECS="${1:-30}"

if [[ $EUID -ne 0 ]]; then
    echo "error: needs root for iptables. run with sudo." >&2
    exit 2
fi
if ! command -v hop-agent >/dev/null; then
    echo "error: hop-agent not in PATH" >&2
    exit 2
fi

# Get control-plane IP from the agent's endpoint config. The "Endpoint:"
# value contains its own colons (https://) so we extract everything after
# the first ": " sequence, not split on every colon.
ENDPOINT=$(hop-agent info --config-dir /etc/hop-agent 2>/dev/null \
    | sed -nE 's/^Endpoint:[[:space:]]+(.*)$/\1/p')
if [[ -z "$ENDPOINT" ]]; then
    echo "error: could not determine control-plane endpoint" >&2
    exit 2
fi
HOST=$(echo "$ENDPOINT" | sed -nE 's,^https?://([^/:]+).*,\1,p')
IP=$(getent hosts "$HOST" | awk '{print $1}' | head -1)
if [[ -z "$IP" ]]; then
    echo "error: could not resolve $HOST" >&2
    exit 2
fi

VERSION=$(hop-agent info --config-dir /etc/hop-agent 2>/dev/null \
    | sed -nE 's/^Version:[[:space:]]+(.*)$/\1/p')

LOG=/var/log/hop-agent.log
[[ -r "$LOG" ]] || LOG=$(systemd-cat-find 2>/dev/null || echo "/var/log/syslog")

echo "==> Test setup"
echo "    agent version: $VERSION"
echo "    control plane: $HOST ($IP)"
echo "    block duration: ${BLOCK_SECS}s"

# Sentinel: rotate logs so we measure ONLY events after this point.
START_TS=$(date +%s)

echo
echo "==> Step 1: ensure agent is running + has at least one healthy heartbeat"
systemctl is-active hop-agent >/dev/null || { echo "agent not running"; exit 2; }
sleep 2  # let any in-flight cycle settle

echo
echo "==> Step 2: block traffic to control plane via iptables (blackhole)"
iptables -I OUTPUT -d "$IP" -j DROP
echo "    blocked at $(date)"

echo
echo "==> Step 3: wait ${BLOCK_SECS}s with traffic dropped"
echo "    (agent's idle keep-alive conns to $IP become silently dead)"
sleep "$BLOCK_SECS"

echo
echo "==> Step 4: unblock traffic"
iptables -D OUTPUT -d "$IP" -j DROP
UNBLOCK_TS=$(date +%s)
echo "    unblocked at $(date)"

echo
echo "==> Step 5: wait up to 5 minutes for next successful heartbeat"
echo "    (decisive test: post-fix should succeed in <30s, pre-fix typically 60-180s)"

DEADLINE=$((UNBLOCK_TS + 300))
SUCCESS_TS=
while (( $(date +%s) < DEADLINE )); do
    # Look for a successful heartbeat after UNBLOCK_TS.
    # Success markers: agent log goes silent (no errors) AND the next renewal
    # cycle log line appears (e.g. "[renew %s] next renewal in"), OR no
    # "context deadline exceeded" in the last 5s.
    if journalctl -u hop-agent --since "@$UNBLOCK_TS" --no-pager 2>/dev/null \
        | grep -E "next renewal in|next heartbeat in|nodes? updated" \
        | head -1 \
        | grep -q .; then
        SUCCESS_TS=$(date +%s)
        break
    fi
    # Fallback: if no errors AND at least 30s elapsed since unblock, call it healthy.
    NOW=$(date +%s)
    if (( NOW - UNBLOCK_TS >= 15 )); then
        FAILS=$(journalctl -u hop-agent --since "@$UNBLOCK_TS" --no-pager 2>/dev/null \
            | grep -c "context deadline exceeded\|heartbeat.*failed" || true)
        if (( FAILS == 0 )); then
            SUCCESS_TS=$NOW
            break
        fi
    fi
    sleep 2
done

echo
echo "==> Step 6: result"
if [[ -z "$SUCCESS_TS" ]]; then
    ELAPSED=$(( $(date +%s) - UNBLOCK_TS ))
    echo "    RESULT: NO RECOVERY in ${ELAPSED}s (>5min) — fix not working OR controller unreachable for unrelated reasons"
    exit 1
fi
ELAPSED=$((SUCCESS_TS - UNBLOCK_TS))
echo "    RESULT: heartbeat recovered in ${ELAPSED}s after route restored"
echo
if (( ELAPSED <= 30 )); then
    echo "    ✓ DECISIVE PASS: ≤30s recovery confirms keep-alives are NOT pooled"
    echo "      (pre-fix expectation was 60-180s)"
    exit 0
elif (( ELAPSED <= 60 )); then
    echo "    ~ MARGINAL: ${ELAPSED}s. May indicate Go runtime cleared the pool naturally; the fix may or may not be the cause."
    exit 0
else
    echo "    ✗ DECISIVE FAIL: ${ELAPSED}s recovery — keep-alive pool is still poisoning heartbeats"
    echo "      The fix has regressed OR this binary is pre-v0.10.31"
    exit 1
fi
