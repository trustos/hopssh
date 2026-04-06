-- name: InsertDNSRecord :exec
INSERT INTO dns_records (id, network_id, name, nebula_ip) VALUES (?, ?, ?, ?);

-- name: ListDNSRecordsForNetwork :many
SELECT id, network_id, name, nebula_ip, created_at
FROM dns_records WHERE network_id = ? ORDER BY name;

-- name: GetDNSRecord :one
SELECT id, network_id, name, nebula_ip, created_at
FROM dns_records WHERE id = ? AND network_id = ?;

-- name: DeleteDNSRecord :exec
DELETE FROM dns_records WHERE id = ? AND network_id = ?;

-- name: DeleteDNSRecordsForNetwork :exec
DELETE FROM dns_records WHERE network_id = ?;

-- name: DNSRecordNameExists :one
SELECT COUNT(*) FROM dns_records WHERE network_id = ? AND name = ?;
