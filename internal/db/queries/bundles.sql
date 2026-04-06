-- name: InsertBundle :exec
INSERT INTO enrollment_bundles (id, node_id, download_token, expires_at)
VALUES (?, ?, ?, ?);

-- name: GetBundleByToken :one
SELECT id, node_id, download_token, downloaded, expires_at, created_at
FROM enrollment_bundles WHERE download_token = ? AND downloaded = 0 AND expires_at > ?;

-- name: MarkBundleDownloaded :exec
UPDATE enrollment_bundles SET downloaded = 1 WHERE id = ?;

-- name: DeleteExpiredBundles :exec
DELETE FROM enrollment_bundles WHERE expires_at < ?;
