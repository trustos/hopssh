-- name: InsertAuditEntry :exec
INSERT INTO audit_log (id, user_id, node_id, network_id, action, details)
VALUES (?, ?, ?, ?, ?, ?);

-- name: ListAuditForNetwork :many
SELECT a.id, a.user_id, a.node_id, a.network_id, a.action, a.details, a.created_at,
       u.email AS user_email, u.name AS user_name,
       n.hostname AS node_hostname
FROM audit_log a
LEFT JOIN users u ON u.id = a.user_id
LEFT JOIN nodes n ON n.id = a.node_id
WHERE a.network_id = sqlc.arg('network_id')
  AND a.created_at >= sqlc.arg('since')
  AND (sqlc.narg('action') IS NULL OR a.action = sqlc.narg('action'))
ORDER BY a.created_at DESC LIMIT sqlc.arg('limit');

-- name: ListAuditForUser :many
SELECT a.id, a.user_id, a.node_id, a.network_id, a.action, a.details, a.created_at,
       u.email AS user_email, u.name AS user_name,
       n.hostname AS node_hostname
FROM audit_log a
LEFT JOIN users u ON u.id = a.user_id
LEFT JOIN nodes n ON n.id = a.node_id
WHERE a.user_id = sqlc.arg('user_id')
  AND a.created_at >= sqlc.arg('since')
  AND (sqlc.narg('action') IS NULL OR a.action = sqlc.narg('action'))
ORDER BY a.created_at DESC LIMIT sqlc.arg('limit');
