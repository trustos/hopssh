-- name: CreateUser :exec
INSERT INTO users (id, email, name, password_hash, github_id)
VALUES (?, ?, ?, ?, ?);

-- name: GetUserByID :one
SELECT id, email, name, password_hash, github_id, created_at
FROM users WHERE id = ?;

-- name: GetUserProfileByID :one
SELECT id, email, name FROM users WHERE id = ?;

-- name: GetUserByEmail :one
SELECT id, email, name, password_hash, github_id, created_at
FROM users WHERE email = ?;

-- name: CountUsers :one
SELECT COUNT(*) FROM users;
