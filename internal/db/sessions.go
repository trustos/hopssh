package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"
)

type Session struct {
	Token     string
	UserID    string
	CreatedAt int64
	ExpiresAt int64
}

type SessionStore struct {
	rdb *sql.DB
	wdb *sql.DB
}

func NewSessionStore(p *DBPair) *SessionStore {
	return &SessionStore{rdb: p.ReadDB, wdb: p.WriteDB}
}

// hashSessionToken returns the hex-encoded SHA-256 hash of a session token.
// Only the hash is stored in the DB; the plaintext is returned to the user.
func hashSessionToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func (s *SessionStore) Create(token, userID string, ttl time.Duration) error {
	_, err := s.wdb.Exec(`
		INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)
	`, hashSessionToken(token), userID, time.Now().Add(ttl).Unix())
	return err
}

func (s *SessionStore) GetUserID(token string) (string, error) {
	var userID string
	err := s.rdb.QueryRow(`
		SELECT user_id FROM sessions WHERE token = ? AND expires_at > ?
	`, hashSessionToken(token), time.Now().Unix()).Scan(&userID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return userID, err
}

func (s *SessionStore) Delete(token string) error {
	_, err := s.wdb.Exec(`DELETE FROM sessions WHERE token = ?`, hashSessionToken(token))
	return err
}

func (s *SessionStore) DeleteExpired() error {
	_, err := s.wdb.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now().Unix())
	return err
}
