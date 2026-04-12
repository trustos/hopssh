package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/trustos/hopssh/internal/db/dbsqlc"
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

func hashSessionToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func (s *SessionStore) Create(token, userID string, ttl time.Duration) error {
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.CreateSession(context.Background(), dbsqlc.CreateSessionParams{
		Token:     hashSessionToken(token),
		UserID:    userID,
		ExpiresAt: time.Now().Add(ttl).Unix(),
	})
}

func (s *SessionStore) GetUserID(token string) (string, error) {
	hash := hashSessionToken(token)
	now := time.Now().Unix()
	params := dbsqlc.GetSessionUserIDParams{Token: hash, ExpiresAt: now}

	// Retry on transient SQLite lock errors. QueryRowContext is not retried
	// by the resilience layer (see resilience.go), so concurrent proxy requests
	// can fail when a write (e.g., audit log) holds the lock.
	var userID string
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		q := dbsqlc.New(WrapDB(s.rdb))
		userID, err = q.GetSessionUserID(context.Background(), params)
		if err == nil {
			return userID, nil
		}
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		if !isLockError(err) {
			return "", err
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "", err
}

func (s *SessionStore) Delete(token string) error {
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.DeleteSession(context.Background(), hashSessionToken(token))
}

func (s *SessionStore) DeleteExpired() error {
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.DeleteExpiredSessions(context.Background(), time.Now().Unix())
}
