-- name: InsertAuditEntry :exec
INSERT INTO audit_log (id, user_id, node_id, network_id, action, details)
VALUES (?, ?, ?, ?, ?, ?);

-- name: ListAuditForNetwork :many
SELECT id, user_id, node_id, network_id, action, details, created_at
FROM audit_log WHERE network_id = ? ORDER BY created_at DESC LIMIT ?;

-- name: ListAuditForUser :many
SELECT id, user_id, node_id, network_id, action, details, created_at
FROM audit_log WHERE user_id = ? ORDER BY created_at DESC LIMIT ?;
