package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/trustos/hopssh/internal/db/dbsqlc"
)

const bundleTTL = 15 * time.Minute

type EnrollmentBundle struct {
	ID            string
	NodeID        string
	DownloadToken string
	Downloaded    bool
	ExpiresAt     int64
	CreatedAt     int64
}

type BundleStore struct {
	rdb *sql.DB
	wdb *sql.DB
}

func NewBundleStore(p *DBPair) *BundleStore {
	return &BundleStore{rdb: p.ReadDB, wdb: p.WriteDB}
}

func (s *BundleStore) Create(id, nodeID string) (*EnrollmentBundle, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate download token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)

	b := &EnrollmentBundle{
		ID:            id,
		NodeID:        nodeID,
		DownloadToken: token,
		ExpiresAt:     time.Now().Add(bundleTTL).Unix(),
	}

	q := dbsqlc.New(s.wdb)
	err := q.InsertBundle(context.Background(), dbsqlc.InsertBundleParams{
		ID:            b.ID,
		NodeID:        b.NodeID,
		DownloadToken: b.DownloadToken,
		ExpiresAt:     b.ExpiresAt,
	})
	if err != nil {
		return nil, fmt.Errorf("create bundle: %w", err)
	}
	return b, nil
}

// ClaimByToken finds a bundle by download token, marks it as downloaded (single-use),
// and returns it. Returns nil if not found, already downloaded, or expired.
func (s *BundleStore) ClaimByToken(token string) (*EnrollmentBundle, error) {
	ctx := context.Background()
	tx, err := s.wdb.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	q := dbsqlc.New(tx)
	row, err := q.GetBundleByToken(ctx, dbsqlc.GetBundleByTokenParams{
		DownloadToken: token,
		ExpiresAt:     time.Now().Unix(),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get bundle: %w", err)
	}

	err = q.MarkBundleDownloaded(ctx, row.ID)
	if err != nil {
		return nil, fmt.Errorf("mark downloaded: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &EnrollmentBundle{
		ID:            row.ID,
		NodeID:        row.NodeID,
		DownloadToken: row.DownloadToken,
		Downloaded:    row.Downloaded != 0,
		ExpiresAt:     row.ExpiresAt,
		CreatedAt:     row.CreatedAt,
	}, nil
}

func (s *BundleStore) DeleteExpired() error {
	q := dbsqlc.New(s.wdb)
	return q.DeleteExpiredBundles(context.Background(), time.Now().Unix())
}
