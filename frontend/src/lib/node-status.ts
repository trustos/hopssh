import type { NodeResponse } from './types/api';

// Must match internal/api/types.go:nodeStaleThreshold (180 seconds /
// 3 minutes). Kept in sync manually — the value almost never changes.
// If it does change, update both locations together.
export const NODE_STALE_THRESHOLD_SEC = 180;

/**
 * Derive the effective display status from the server-reported status
 * plus the freshness of the last heartbeat. Mirrors the server's
 * `effectiveStatus()` in `internal/api/types.go` so a fresh page load
 * and a tab that's been open for 10 minutes show the same thing.
 *
 * Possible return values:
 *   - "online"   — heartbeat fresh, peer state OK.
 *   - "degraded" — heartbeat fresh but node has zero peers despite
 *                   other nodes being available; signals a portmap/NAT
 *                   failure that the plain-HTTPS heartbeat can't
 *                   detect. Server computes this (needs cross-node
 *                   context); client passes it through. Decays to
 *                   "offline" when heartbeat itself goes stale.
 *   - "offline"  — heartbeat stale past NODE_STALE_THRESHOLD_SEC.
 *   - "pending" / "enrolled" — enrollment lifecycle states, untouched.
 *
 * @param node       the node (only `status` and `lastSeenAt` are needed)
 * @param nowSeconds current UNIX-epoch seconds — use the reactive `now`
 *                    state variable on the page so the return value
 *                    recomputes every tick
 */
export function displayStatus(
	node: Pick<NodeResponse, 'status' | 'lastSeenAt'>,
	nowSeconds: number,
): string {
	if ((node.status !== 'online' && node.status !== 'degraded') || !node.lastSeenAt) {
		return node.status;
	}
	if (nowSeconds - node.lastSeenAt > NODE_STALE_THRESHOLD_SEC) {
		return 'offline';
	}
	return node.status;
}
