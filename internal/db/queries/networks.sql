-- name: InsertNetwork :exec
INSERT INTO networks (id, user_id, name, slug, nebula_ca_cert, nebula_ca_key, nebula_subnet, server_cert, server_key)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetNetworkByID :one
SELECT id, user_id, name, slug, nebula_ca_cert, nebula_ca_key, nebula_subnet, server_cert, server_key, created_at
FROM networks WHERE id = ?;

-- name: ListNetworksForUser :many
SELECT id, user_id, name, slug, nebula_subnet, created_at
FROM networks WHERE user_id = ? ORDER BY created_at DESC;

-- name: NetworkSlugExists :one
SELECT COUNT(*) FROM networks WHERE slug = ?;

-- name: DeleteNetwork :exec
DELETE FROM networks WHERE id = ?;

-- name: MaxSubnetOctet :one
SELECT MAX(CAST(SUBSTR(nebula_subnet, 7, INSTR(SUBSTR(nebula_subnet, 7), '.') - 1) AS INTEGER))
FROM networks WHERE nebula_subnet IS NOT NULL;
