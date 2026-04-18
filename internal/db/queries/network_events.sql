-- name: InsertNetworkEvent :exec
INSERT INTO network_events (network_id, event_type, target_id, status, details, created_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: ListNetworkEvents :many
SELECT e.id, e.network_id, e.event_type, e.target_id, e.status, e.details, e.created_at,
       n.hostname AS target_hostname
FROM network_events e
LEFT JOIN nodes n ON n.id = e.target_id
WHERE e.network_id = sqlc.arg('network_id')
  AND e.created_at >= sqlc.arg('since')
  AND (sqlc.narg('event_type') IS NULL OR e.event_type = sqlc.narg('event_type'))
ORDER BY e.created_at DESC, e.id DESC
LIMIT sqlc.arg('limit');

-- name: ScanStaleOnlineNodes :many
SELECT id, network_id, hostname, last_seen_at
FROM nodes
WHERE status = 'online'
  AND node_type != 'lighthouse'
  AND (last_seen_at IS NULL OR last_seen_at < sqlc.arg('cutoff'));
