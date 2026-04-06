package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/trustos/hopssh/internal/db/dbsqlc"
)

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

func hashDeviceCode(code string) string {
	h := sha256.Sum256([]byte(code))
	return hex.EncodeToString(h[:])
}

// Create generates a new device code pair and stores it.
func (s *DeviceCodeStore) Create() (*DeviceCode, error) {
	deviceCode, err := generateDeviceCode()
	if err != nil {
		return nil, err
	}

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

		q := dbsqlc.New(s.wdb)
		err = q.InsertDeviceCode(context.Background(), dbsqlc.InsertDeviceCodeParams{
			DeviceCode: hashDeviceCode(deviceCode),
			UserCode:   dc.UserCode,
			Status:     dc.Status,
			ExpiresAt:  dc.ExpiresAt,
		})
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				continue
			}
			return nil, fmt.Errorf("create device code: %w", err)
		}
		return dc, nil
	}
	return nil, fmt.Errorf("failed to generate unique user code after 3 attempts")
}

// GetByDeviceCode returns a device code by its device code (used by agent polling).
func (s *DeviceCodeStore) GetByDeviceCode(deviceCode string) (*DeviceCode, error) {
	q := dbsqlc.New(s.rdb)
	row, err := q.GetDeviceCodeByCode(context.Background(), hashDeviceCode(deviceCode))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get device code: %w", err)
	}
	return mapDeviceCodeRow(row), nil
}

// GetByUserCode returns a device code by its user code (used by browser authorization).
func (s *DeviceCodeStore) GetByUserCode(userCode string) (*DeviceCode, error) {
	q := dbsqlc.New(s.rdb)
	row, err := q.GetDeviceCodeByUserCode(context.Background(), dbsqlc.GetDeviceCodeByUserCodeParams{
		UserCode:  strings.ToUpper(userCode),
		ExpiresAt: time.Now().Unix(),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get device code by user code: %w", err)
	}
	return mapDeviceCodeRow(row), nil
}

// Authorize marks a device code as authorized by a user for a specific network.
func (s *DeviceCodeStore) Authorize(userCode, userID, networkID string) error {
	q := dbsqlc.New(s.wdb)
	result, err := q.AuthorizeDeviceCode(context.Background(), dbsqlc.AuthorizeDeviceCodeParams{
		UserID:    &userID,
		NetworkID: &networkID,
		UserCode:  strings.ToUpper(userCode),
		ExpiresAt: time.Now().Unix(),
	})
	if err != nil {
		return fmt.Errorf("authorize device code: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("device code not found, already used, or expired")
	}
	return nil
}

// ClaimAuthorized atomically transitions an authorized device code to completed.
func (s *DeviceCodeStore) ClaimAuthorized(deviceCode string) (*DeviceCode, error) {
	h := hashDeviceCode(deviceCode)
	ctx := context.Background()

	tx, err := s.wdb.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	q := dbsqlc.New(tx)
	row, err := q.GetAuthorizedDeviceCode(ctx, dbsqlc.GetAuthorizedDeviceCodeParams{
		DeviceCode: h,
		ExpiresAt:  time.Now().Unix(),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query device code: %w", err)
	}

	result, err := q.CompleteDeviceCode(ctx, h)
	if err != nil {
		return nil, fmt.Errorf("claim device code: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return nil, nil
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return &DeviceCode{
		DeviceCode: row.DeviceCode,
		UserCode:   row.UserCode,
		UserID:     row.UserID,
		NetworkID:  row.NetworkID,
		Status:     row.Status,
		ExpiresAt:  row.ExpiresAt,
	}, nil
}

// SetNodeID updates the node_id after enrollment completes.
func (s *DeviceCodeStore) SetNodeID(deviceCode, nodeID string) error {
	q := dbsqlc.New(s.wdb)
	return q.SetDeviceCodeNodeID(context.Background(), dbsqlc.SetDeviceCodeNodeIDParams{
		NodeID:     &nodeID,
		DeviceCode: hashDeviceCode(deviceCode),
	})
}

// DeleteExpired removes expired device codes.
func (s *DeviceCodeStore) DeleteExpired() error {
	q := dbsqlc.New(s.wdb)
	return q.DeleteExpiredDeviceCodes(context.Background(), time.Now().Unix())
}

func mapDeviceCodeRow(row dbsqlc.DeviceCode) *DeviceCode {
	return &DeviceCode{
		DeviceCode: row.DeviceCode,
		UserCode:   row.UserCode,
		UserID:     row.UserID,
		NetworkID:  row.NetworkID,
		NodeID:     row.NodeID,
		Status:     row.Status,
		ExpiresAt:  row.ExpiresAt,
		CreatedAt:  row.CreatedAt,
	}
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
