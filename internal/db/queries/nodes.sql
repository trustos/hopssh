-- name: InsertNode :exec
INSERT INTO nodes (id, network_id, hostname, os, arch, nebula_cert, nebula_key, nebula_ip, agent_token, enrollment_token, enrollment_expires_at, status)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetNodeByID :one
SELECT id, network_id, hostname, os, arch, nebula_cert, nebula_key, nebula_ip,
       agent_token, enrollment_token, agent_real_ip, status, last_seen_at, created_at
FROM nodes WHERE id = ?;

-- name: ListNodesForNetwork :many
SELECT id, network_id, hostname, os, arch, nebula_ip, agent_real_ip, status, last_seen_at, created_at
FROM nodes WHERE network_id = ? ORDER BY created_at ASC;

-- name: CountNodesForNetwork :one
SELECT COUNT(*) FROM nodes WHERE network_id = ?;

-- name: ListNodeIPsForNetwork :many
SELECT nebula_ip FROM nodes WHERE network_id = ? AND nebula_ip IS NOT NULL;

-- name: GetNodeByEnrollmentToken :one
SELECT id, network_id, hostname, os, arch, nebula_ip, agent_token, status
FROM nodes WHERE enrollment_token = ?
  AND (enrollment_expires_at IS NULL OR enrollment_expires_at > ?);

-- name: ConsumeEnrollmentToken :execresult
UPDATE nodes SET enrollment_token = NULL, enrollment_expires_at = NULL
WHERE id = ? AND enrollment_token = ?;

-- name: CompleteEnrollment :exec
UPDATE nodes SET nebula_cert = ?, nebula_key = ?, hostname = ?, os = ?, arch = ?,
status = 'enrolled'
WHERE id = ?;

-- name: UpdateNodeCert :exec
UPDATE nodes SET nebula_cert = ?, nebula_key = ? WHERE id = ?;

-- name: UpdateNodeStatus :exec
UPDATE nodes SET status = ? WHERE id = ?;

-- name: UpdateNodeLastSeen :exec
UPDATE nodes SET last_seen_at = unixepoch(), status = 'online' WHERE id = ?;

-- name: UpdateNodeAgentRealIP :exec
UPDATE nodes SET agent_real_ip = ? WHERE id = ?;

-- name: DeleteNode :exec
DELETE FROM nodes WHERE id = ?;

-- name: DeleteNodesForNetwork :exec
DELETE FROM nodes WHERE network_id = ?;
