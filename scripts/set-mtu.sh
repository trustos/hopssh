#!/usr/bin/env bash
# set-mtu.sh — Override tun.mtu in every enrollment's nebula.yaml on
# both Macs (mini local + laptop SSH), then restart the agent. Used by
# the WiFi comparison protocol (Test 2: MTU bisection).
#
# Usage:
#   scripts/set-mtu.sh 1280              # set MTU on both hosts
#   scripts/set-mtu.sh --restore         # revert to nebulacfg.TunMTU (1420)
#   LAPTOP_HOST=192.168.x.y scripts/set-mtu.sh 1380
#
# Idempotent: if the requested MTU is already in place, no restart.
set -euo pipefail

LAPTOP_USER="${LAPTOP_USER:-yavortenev}"
LAPTOP_HOST="${LAPTOP_HOST:-192.168.23.18}"
LAPTOP_SSH="${LAPTOP_USER}@${LAPTOP_HOST}"
SERVICE_NAME="com.hopssh.agent"
SERVICE_PLIST="/Library/LaunchDaemons/${SERVICE_NAME}.plist"
CONFIG_DIR="/etc/hop-agent"
DEFAULT_MTU=1420

if [[ $# -ne 1 ]]; then
    echo "usage: $0 <mtu>|--restore" >&2
    exit 2
fi

target="$1"
if [[ "$target" == "--restore" ]]; then
    target="$DEFAULT_MTU"
fi
if ! [[ "$target" =~ ^[0-9]+$ ]] || (( target < 576 || target > 9000 )); then
    echo "error: MTU must be an integer in [576,9000], got: $target" >&2
    exit 2
fi

# Build a sed-on-host snippet that handles both "    mtu: 1234" and
# "  mtu: 1234" indentation; nebulacfg writes 4-space indent under tun:.
apply_mtu() {
    local mtu="$1"
    # Use awk to process every nebula.yaml under $CONFIG_DIR/*/.
    sudo find "$CONFIG_DIR" -mindepth 2 -maxdepth 2 -name nebula.yaml | \
        while read -r yaml; do
            current=$(sudo awk '
                /^tun:/{intun=1; next}
                intun && /^[a-z]/{intun=0}
                intun && /^[[:space:]]+mtu:/{print $2; exit}
            ' "$yaml")
            if [[ "$current" == "$mtu" ]]; then
                echo "    $yaml: already at $mtu"
            else
                sudo awk -v new="$mtu" '
                    /^tun:/{intun=1; print; next}
                    intun && /^[a-z]/{intun=0}
                    intun && /^[[:space:]]+mtu:/{
                        match($0, /^[[:space:]]+/);
                        printf "%smtu: %d\n", substr($0,1,RLENGTH), new;
                        next
                    }
                    {print}
                ' "$yaml" | sudo tee "${yaml}.new" >/dev/null
                sudo mv "${yaml}.new" "$yaml"
                echo "    $yaml: $current -> $mtu"
            fi
        done
}

restart_local() {
    sudo launchctl bootout "system/${SERVICE_NAME}" 2>/dev/null || true
    sleep 1
    sudo launchctl bootstrap system "${SERVICE_PLIST}"
}

echo "==> Mac mini (local): setting MTU to $target"
apply_mtu "$target"
restart_local
echo "    restarted"

echo "==> Laptop (${LAPTOP_HOST}): setting MTU to $target"
ssh "${LAPTOP_SSH}" "MTU=$target bash -s" <<'REMOTE'
set -euo pipefail
SERVICE_NAME="com.hopssh.agent"
SERVICE_PLIST="/Library/LaunchDaemons/${SERVICE_NAME}.plist"
CONFIG_DIR="/etc/hop-agent"
sudo find "$CONFIG_DIR" -mindepth 2 -maxdepth 2 -name nebula.yaml | while read -r yaml; do
    current=$(sudo awk '/^tun:/{intun=1;next} intun && /^[a-z]/{intun=0} intun && /^[[:space:]]+mtu:/{print $2; exit}' "$yaml")
    if [[ "$current" == "$MTU" ]]; then
        echo "    $yaml: already at $MTU"
    else
        sudo awk -v new="$MTU" '
            /^tun:/{intun=1; print; next}
            intun && /^[a-z]/{intun=0}
            intun && /^[[:space:]]+mtu:/{ match($0,/^[[:space:]]+/); printf "%smtu: %d\n", substr($0,1,RLENGTH), new; next }
            {print}
        ' "$yaml" | sudo tee "${yaml}.new" >/dev/null
        sudo mv "${yaml}.new" "$yaml"
        echo "    $yaml: $current -> $MTU"
    fi
done
sudo launchctl bootout "system/${SERVICE_NAME}" 2>/dev/null || true
sleep 1
sudo launchctl bootstrap system "${SERVICE_PLIST}"
echo "    restarted"
REMOTE

echo "==> Verifying tunnels recover (10s)..."
sleep 10
echo "==> Done. MTU set to $target on both hosts."
