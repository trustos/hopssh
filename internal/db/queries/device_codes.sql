-- name: InsertDeviceCode :exec
INSERT INTO device_codes (device_code, user_code, status, expires_at)
VALUES (?, ?, ?, ?);

-- name: GetDeviceCodeByCode :one
SELECT device_code, user_code, user_id, network_id, node_id, status, expires_at, created_at
FROM device_codes WHERE device_code = ?;

-- name: GetDeviceCodeByUserCode :one
SELECT device_code, user_code, user_id, network_id, node_id, status, expires_at, created_at
FROM device_codes WHERE user_code = ? AND expires_at > ?;

-- name: AuthorizeDeviceCode :execresult
UPDATE device_codes SET user_id = ?, network_id = ?, status = 'authorized'
WHERE user_code = ? AND status = 'pending' AND expires_at > ?;

-- name: GetAuthorizedDeviceCode :one
SELECT device_code, user_code, user_id, network_id, status, expires_at
FROM device_codes WHERE device_code = ? AND status = 'authorized' AND expires_at > ?;

-- name: CompleteDeviceCode :execresult
UPDATE device_codes SET status = 'completed' WHERE device_code = ? AND status = 'authorized';

-- name: SetDeviceCodeNodeID :exec
UPDATE device_codes SET node_id = ? WHERE device_code = ?;

-- name: DeleteExpiredDeviceCodes :exec
DELETE FROM device_codes WHERE expires_at < ?;
