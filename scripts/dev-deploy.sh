#!/usr/bin/env bash
# dev-deploy.sh — Build and deploy agent to both Macs for local testing.
# Usage: make dev-deploy
set -euo pipefail

LAPTOP_USER="yavortenev"
LAPTOP_HOST="192.168.23.18"
LAPTOP_SSH="${LAPTOP_USER}@${LAPTOP_HOST}"
AGENT_PATH="/usr/local/bin/hop-agent"
SERVICE_NAME="com.hopssh.agent"
SERVICE_PLIST="/Library/LaunchDaemons/${SERVICE_NAME}.plist"

echo "==> Building agent..."
make build

echo "==> Deploying to Mac mini (local)..."
sudo /bin/cp hop-agent "${AGENT_PATH}"
sudo launchctl bootout "system/${SERVICE_NAME}" 2>/dev/null || true
sleep 1
sudo launchctl bootstrap system "${SERVICE_PLIST}"
echo "    Mac mini: deployed + restarted"

echo "==> Deploying to laptop (${LAPTOP_HOST})..."
scp -q hop-agent "${LAPTOP_SSH}:/tmp/hop-agent"
ssh "${LAPTOP_SSH}" "
    sudo /bin/cp /tmp/hop-agent ${AGENT_PATH} && \
    sudo chmod +x ${AGENT_PATH} && \
    sudo launchctl bootout system/${SERVICE_NAME} 2>/dev/null; \
    sleep 1; \
    sudo launchctl bootstrap system ${SERVICE_PLIST} && \
    echo '    Laptop: deployed + restarted'
"

echo "==> Verifying..."
sleep 3
echo "Mac mini: $(${AGENT_PATH} version 2>&1)"
echo "Laptop:   $(ssh ${LAPTOP_SSH} "${AGENT_PATH} version" 2>&1)"
echo "==> Done."
