-- name: InsertNetworkMember :exec
INSERT INTO network_members (id, network_id, user_id, role)
VALUES (?, ?, ?, ?);

-- name: GetNetworkMember :one
SELECT id, network_id, user_id, role, created_at
FROM network_members WHERE network_id = ? AND user_id = ?;

-- name: ListMembersForNetwork :many
SELECT nm.id, nm.network_id, nm.user_id, nm.role, nm.created_at,
       u.email, u.name
FROM network_members nm
JOIN users u ON u.id = nm.user_id
WHERE nm.network_id = ?
ORDER BY nm.created_at;

-- name: ListNetworkIDsForMember :many
SELECT network_id FROM network_members WHERE user_id = ?;

-- name: DeleteNetworkMember :exec
DELETE FROM network_members WHERE id = ?;

-- name: DeleteNetworkMemberByNetworkAndUser :exec
DELETE FROM network_members WHERE network_id = ? AND user_id = ?;
