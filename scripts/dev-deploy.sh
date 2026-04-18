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
# ditto --noextattr instead of cp: a local cp carries xattrs from the
# build directory into /usr/local/bin, and launchd's AMFI then refuses
# to load the LaunchDaemon with OS_REASON_CODESIGNING. scp to the laptop
# strips xattrs in transit so `cp` is fine there.
sudo /usr/bin/ditto --noextattr hop-agent "${AGENT_PATH}"
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
