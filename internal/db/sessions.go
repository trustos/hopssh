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
	q := dbsqlc.New(s.wdb)
	return q.CreateSession(context.Background(), dbsqlc.CreateSessionParams{
		Token:     hashSessionToken(token),
		UserID:    userID,
		ExpiresAt: time.Now().Add(ttl).Unix(),
	})
}

func (s *SessionStore) GetUserID(token string) (string, error) {
	q := dbsqlc.New(s.rdb)
	userID, err := q.GetSessionUserID(context.Background(), dbsqlc.GetSessionUserIDParams{
		Token:     hashSessionToken(token),
		ExpiresAt: time.Now().Unix(),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return userID, err
}

func (s *SessionStore) Delete(token string) error {
	q := dbsqlc.New(s.wdb)
	return q.DeleteSession(context.Background(), hashSessionToken(token))
}

func (s *SessionStore) DeleteExpired() error {
	q := dbsqlc.New(s.wdb)
	return q.DeleteExpiredSessions(context.Background(), time.Now().Unix())
}
