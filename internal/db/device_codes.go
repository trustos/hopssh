package db

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"
)

func hashDeviceCode(code string) string {
	h := sha256.Sum256([]byte(code))
	return hex.EncodeToString(h[:])
}

type DeviceCode struct {
	DeviceCode string
	UserCode   string
	UserID     *string
	NetworkID  *string
	NodeID     *string
	Status     string // pending, authorized, completed, expired
	ExpiresAt  int64
	CreatedAt  int64
}

type DeviceCodeStore struct {
	rdb *sql.DB
	wdb *sql.DB
}

func NewDeviceCodeStore(p *DBPair) *DeviceCodeStore {
	return &DeviceCodeStore{rdb: p.ReadDB, wdb: p.WriteDB}
}

const (
	deviceCodeTTL    = 10 * time.Minute
	userCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/O/1/I
	userCodeLength   = 4
)

// Create generates a new device code pair and stores it.
// Returns the device code (for agent polling) and user code (for human entry).
func (s *DeviceCodeStore) Create() (*DeviceCode, error) {
	deviceCode, err := generateDeviceCode()
	if err != nil {
		return nil, err
	}

	// Retry on user code collision (small namespace: 32^4 ≈ 1M).
	for attempt := 0; attempt < 3; attempt++ {
		userCode, err := generateUserCode()
		if err != nil {
			return nil, err
		}

		dc := &DeviceCode{
			DeviceCode: deviceCode, // plaintext returned to caller
			UserCode:   "HOP-" + userCode,
			Status:     "pending",
			ExpiresAt:  time.Now().Add(deviceCodeTTL).Unix(),
		}

		_, err = s.wdb.Exec(`
			INSERT INTO device_codes (device_code, user_code, status, expires_at)
			VALUES (?, ?, ?, ?)
		`, hashDeviceCode(deviceCode), dc.UserCode, dc.Status, dc.ExpiresAt)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				continue // user_code collision, retry
			}
			return nil, fmt.Errorf("create device code: %w", err)
		}
		return dc, nil
	}
	return nil, fmt.Errorf("failed to generate unique user code after 3 attempts")
}

// GetByDeviceCode returns a device code by its device code (used by agent polling).
func (s *DeviceCodeStore) GetByDeviceCode(deviceCode string) (*DeviceCode, error) {
	var dc DeviceCode
	h := hashDeviceCode(deviceCode)
	err := s.rdb.QueryRow(`
		SELECT device_code, user_code, user_id, network_id, node_id, status, expires_at, created_at
		FROM device_codes WHERE device_code = ?
	`, h).Scan(&dc.DeviceCode, &dc.UserCode, &dc.UserID, &dc.NetworkID,
		&dc.NodeID, &dc.Status, &dc.ExpiresAt, &dc.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get device code: %w", err)
	}
	return &dc, nil
}

// GetByUserCode returns a device code by its user code (used by browser authorization).
func (s *DeviceCodeStore) GetByUserCode(userCode string) (*DeviceCode, error) {
	var dc DeviceCode
	err := s.rdb.QueryRow(`
		SELECT device_code, user_code, user_id, network_id, node_id, status, expires_at, created_at
		FROM device_codes WHERE user_code = ? AND expires_at > ?
	`, strings.ToUpper(userCode), time.Now().Unix()).Scan(&dc.DeviceCode, &dc.UserCode,
		&dc.UserID, &dc.NetworkID, &dc.NodeID, &dc.Status, &dc.ExpiresAt, &dc.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get device code by user code: %w", err)
	}
	return &dc, nil
}

// Authorize marks a device code as authorized by a user for a specific network.
func (s *DeviceCodeStore) Authorize(userCode, userID, networkID string) error {
	res, err := s.wdb.Exec(`
		UPDATE device_codes SET user_id = ?, network_id = ?, status = 'authorized'
		WHERE user_code = ? AND status = 'pending' AND expires_at > ?
	`, userID, networkID, strings.ToUpper(userCode), time.Now().Unix())
	if err != nil {
		return fmt.Errorf("authorize device code: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("device code not found, already used, or expired")
	}
	return nil
}

// ClaimAuthorized atomically transitions an authorized device code to completed.
// Returns the device code data if successful, nil if already claimed or not authorized.
func (s *DeviceCodeStore) ClaimAuthorized(deviceCode string) (*DeviceCode, error) {
	h := hashDeviceCode(deviceCode)
	tx, err := s.wdb.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var dc DeviceCode
	err = tx.QueryRow(`
		SELECT device_code, user_code, user_id, network_id, status, expires_at
		FROM device_codes WHERE device_code = ? AND status = 'authorized' AND expires_at > ?
	`, h, time.Now().Unix()).Scan(&dc.DeviceCode, &dc.UserCode, &dc.UserID,
		&dc.NetworkID, &dc.Status, &dc.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query device code: %w", err)
	}

	res, err := tx.Exec(`
		UPDATE device_codes SET status = 'completed' WHERE device_code = ? AND status = 'authorized'
	`, h)
	if err != nil {
		return nil, fmt.Errorf("claim device code: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return nil, nil // raced with another poll
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return &dc, nil
}

// SetNodeID updates the node_id after enrollment completes.
func (s *DeviceCodeStore) SetNodeID(deviceCode, nodeID string) error {
	_, err := s.wdb.Exec(`UPDATE device_codes SET node_id = ? WHERE device_code = ?`, nodeID, hashDeviceCode(deviceCode))
	return err
}

// DeleteExpired removes expired device codes.
func (s *DeviceCodeStore) DeleteExpired() error {
	_, err := s.wdb.Exec(`DELETE FROM device_codes WHERE expires_at < ?`, time.Now().Unix())
	return err
}

func generateDeviceCode() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func generateUserCode() (string, error) {
	code := make([]byte, userCodeLength)
	for i := range code {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(userCodeAlphabet))))
		if err != nil {
			return "", err
		}
		code[i] = userCodeAlphabet[n.Int64()]
	}
	return string(code), nil
}
