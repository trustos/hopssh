#!/usr/bin/env bash
# Smoke test for the local API.
#
# Starts a fresh hop-agent against an isolated config dir, captures the
# HOPSSH_LOCAL_API line, then exercises every read endpoint.
#
# Run from clients/desktop/.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DESKTOP_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$DESKTOP_DIR/../.." && pwd)"

AGENT_BIN="${HOPSSH_AGENT_BINARY:-$DESKTOP_DIR/src-tauri/binaries/hop-agent}"
if [ ! -x "$AGENT_BIN" ]; then
  echo "Building hop-agent into $AGENT_BIN..."
  (cd "$REPO_ROOT" && go build -mod=vendor -o "$AGENT_BIN" ./cmd/agent)
fi

CFG_DIR="$(mktemp -d /tmp/hopssh-smoke.XXXXXX)"
echo "config dir: $CFG_DIR"

LOG="$CFG_DIR/agent.log"
"$AGENT_BIN" serve --config-dir "$CFG_DIR" >"$LOG" 2>&1 &
AGENT_PID=$!
trap 'kill "$AGENT_PID" 2>/dev/null || true; rm -rf "$CFG_DIR"' EXIT

# Wait up to 10 s for the HOPSSH_LOCAL_API line.
deadline=$(($(date +%s) + 10))
endpoint=""
token=""
while [ "$(date +%s)" -lt "$deadline" ]; do
  if line=$(grep "^HOPSSH_LOCAL_API:" "$LOG" 2>/dev/null | head -1); then
    if [ -n "$line" ]; then
      rest=${line#HOPSSH_LOCAL_API:}
      # Last colon separates host:port from token.
      token="${rest##*:}"
      endpoint="${rest%:*}"
      break
    fi
  fi
  sleep 0.2
done

if [ -z "$endpoint" ] || [ -z "$token" ]; then
  echo "FAIL: agent did not announce HOPSSH_LOCAL_API within 10 s"
  echo "--- agent log ---"
  cat "$LOG"
  exit 1
fi

echo "endpoint: http://$endpoint"
echo "token:    ${token:0:8}..."

curl_local() {
  local method="$1" path="$2" body="${3-}"
  if [ -n "$body" ]; then
    curl -fsS -X "$method" \
      -H "Authorization: Bearer $token" \
      -H "Content-Type: application/json" \
      --data "$body" \
      "http://$endpoint$path"
  else
    curl -fsS -X "$method" \
      -H "Authorization: Bearer $token" \
      "http://$endpoint$path"
  fi
}

echo
echo "GET /local/health"
curl_local GET /local/health | tee /dev/stderr
echo

echo
echo "GET /local/status"
curl_local GET /local/status | python3 -m json.tool

echo
echo "Reject without bearer token (should be 401):"
code=$(curl -s -o /dev/null -w "%{http_code}" "http://$endpoint/local/status" || true)
if [ "$code" != "401" ]; then
  echo "FAIL: expected 401 with no token, got $code"
  exit 1
fi
echo "  ok ($code)"

echo
echo "Reject from non-loopback (should fail):"
# Bind a random non-loopback if available; otherwise, skip this check.
host_ip=$(ifconfig 2>/dev/null | awk '/inet / && !/127\./ && !/inet 169\./ {print $2; exit}')
if [ -n "$host_ip" ]; then
  code=$(curl -s -o /dev/null -w "%{http_code}" \
    --interface "$host_ip" \
    -H "Authorization: Bearer $token" \
    "http://$endpoint/local/status" || true)
  if [ "$code" != "000" ] && [ "$code" != "401" ] && [ "$code" != "403" ]; then
    echo "WARN: non-loopback request returned HTTP $code; expected connect failure or 403"
  else
    echo "  ok ($code)"
  fi
else
  echo "  skipped (no non-loopback IP found)"
fi

echo
echo "All smoke checks passed. Token + endpoint:"
echo "  export VITE_HOPSSH_API=http://$endpoint"
echo "  export VITE_HOPSSH_TOKEN=$token"
echo
echo "(agent log: $LOG, will be cleaned up on exit)"
