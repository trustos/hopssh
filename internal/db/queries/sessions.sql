-- name: CreateSession :exec
INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?);

-- name: GetSessionUserID :one
SELECT user_id FROM sessions WHERE token = ? AND expires_at > ?;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE token = ?;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at < ?;
