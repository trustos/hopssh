-- name: InsertNetwork :exec
INSERT INTO networks (id, user_id, name, slug, nebula_ca_cert, nebula_ca_key, nebula_subnet, server_cert, server_key, lighthouse_port, dns_domain)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetNetworkByID :one
SELECT id, user_id, name, slug, nebula_ca_cert, nebula_ca_key, nebula_subnet, server_cert, server_key, lighthouse_port, dns_domain, created_at
FROM networks WHERE id = ?;

-- name: ListNetworksForUser :many
SELECT id, user_id, name, slug, nebula_subnet, lighthouse_port, dns_domain, created_at
FROM networks WHERE user_id = ? ORDER BY created_at DESC;

-- name: ListAllNetworks :many
SELECT id, user_id, name, slug, nebula_ca_cert, nebula_ca_key, nebula_subnet, server_cert, server_key, lighthouse_port, dns_domain, created_at
FROM networks;

-- name: NetworkSlugExists :one
SELECT COUNT(*) FROM networks WHERE slug = ?;

-- name: DeleteNetwork :exec
DELETE FROM networks WHERE id = ?;

-- name: MaxSubnetOctet :one
SELECT MAX(CAST(SUBSTR(nebula_subnet, 7, INSTR(SUBSTR(nebula_subnet, 7), '.') - 1) AS INTEGER))
FROM networks WHERE nebula_subnet IS NOT NULL;

-- name: MaxLighthousePort :one
SELECT MAX(lighthouse_port) FROM networks WHERE lighthouse_port IS NOT NULL;

-- name: FirstAvailableLighthousePort :one
SELECT COALESCE(
    (SELECT n1.lighthouse_port + 1
     FROM networks n1
     WHERE n1.lighthouse_port IS NOT NULL
     AND NOT EXISTS (
         SELECT 1 FROM networks n2
         WHERE n2.lighthouse_port = n1.lighthouse_port + 1
     )
     ORDER BY n1.lighthouse_port
     LIMIT 1),
    42001
) AS next_port;
