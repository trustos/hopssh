-- name: InsertInvite :exec
INSERT INTO network_invites (id, network_id, created_by, code, role, max_uses, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetInviteByCode :one
SELECT id, network_id, created_by, code, role, max_uses, use_count, expires_at, created_at
FROM network_invites WHERE code = ?;

-- name: IncrementInviteUseCount :exec
UPDATE network_invites SET use_count = use_count + 1 WHERE id = ?;

-- name: AtomicClaimInvite :execresult
UPDATE network_invites SET use_count = use_count + 1
WHERE code = ?
  AND (expires_at IS NULL OR expires_at > unixepoch())
  AND (max_uses IS NULL OR use_count < max_uses);

-- name: ListInvitesForNetwork :many
SELECT id, network_id, created_by, code, role, max_uses, use_count, expires_at, created_at
FROM network_invites WHERE network_id = ? ORDER BY created_at DESC;

-- name: DeleteInvite :exec
DELETE FROM network_invites WHERE id = ?;

-- name: DeleteExpiredInvites :exec
DELETE FROM network_invites
WHERE expires_at IS NOT NULL AND expires_at < unixepoch();
