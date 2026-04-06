package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
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

// Create generates a new bundle record with a crypto-random download token.
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

	_, err := s.wdb.Exec(`
		INSERT INTO enrollment_bundles (id, node_id, download_token, expires_at)
		VALUES (?, ?, ?, ?)
	`, b.ID, b.NodeID, b.DownloadToken, b.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("create bundle: %w", err)
	}
	return b, nil
}

// ClaimByToken finds a bundle by download token, marks it as downloaded (single-use),
// and returns it. Returns nil if not found, already downloaded, or expired.
func (s *BundleStore) ClaimByToken(token string) (*EnrollmentBundle, error) {
	tx, err := s.wdb.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var b EnrollmentBundle
	err = tx.QueryRow(`
		SELECT id, node_id, download_token, downloaded, expires_at, created_at
		FROM enrollment_bundles WHERE download_token = ? AND downloaded = 0 AND expires_at > ?
	`, token, time.Now().Unix()).Scan(&b.ID, &b.NodeID, &b.DownloadToken,
		&b.Downloaded, &b.ExpiresAt, &b.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get bundle: %w", err)
	}

	_, err = tx.Exec(`UPDATE enrollment_bundles SET downloaded = 1 WHERE id = ?`, b.ID)
	if err != nil {
		return nil, fmt.Errorf("mark downloaded: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &b, nil
}

// DeleteExpired removes expired bundles.
func (s *BundleStore) DeleteExpired() error {
	_, err := s.wdb.Exec(`DELETE FROM enrollment_bundles WHERE expires_at < ?`, time.Now().Unix())
	return err
}
