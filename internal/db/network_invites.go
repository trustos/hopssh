package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/trustos/hopssh/internal/db/dbsqlc"
)

// NetworkInvite represents an invite code for joining a network.
type NetworkInvite struct {
	ID        string
	NetworkID string
	CreatedBy string
	Code      string
	Role      string
	MaxUses   *int64
	UseCount  int64
	ExpiresAt *int64
	CreatedAt int64
}

type InviteStore struct {
	rdb *sql.DB
	wdb *sql.DB
}

func NewInviteStore(d *DBPair) *InviteStore {
	return &InviteStore{rdb: d.ReadDB, wdb: d.WriteDB}
}

func (s *InviteStore) Create(invite *NetworkInvite) error {
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.InsertInvite(context.Background(), dbsqlc.InsertInviteParams{
		ID:        invite.ID,
		NetworkID: invite.NetworkID,
		CreatedBy: invite.CreatedBy,
		Code:      invite.Code,
		Role:      invite.Role,
		MaxUses:   invite.MaxUses,
		ExpiresAt: invite.ExpiresAt,
	})
}

func (s *InviteStore) GetByCode(code string) (*NetworkInvite, error) {
	q := dbsqlc.New(WrapDB(s.rdb))
	row, err := q.GetInviteByCode(context.Background(), code)
	if err != nil {
		return nil, err
	}
	return &NetworkInvite{
		ID:        row.ID,
		NetworkID: row.NetworkID,
		CreatedBy: row.CreatedBy,
		Code:      row.Code,
		Role:      row.Role,
		MaxUses:   row.MaxUses,
		UseCount:  row.UseCount,
		ExpiresAt: row.ExpiresAt,
		CreatedAt: row.CreatedAt,
	}, nil
}

// Claim atomically validates and consumes one use of an invite.
// Uses a single UPDATE with WHERE conditions to prevent TOCTOU races.
// Returns the invite if valid, or an error describing why it's invalid.
func (s *InviteStore) Claim(code string) (*NetworkInvite, error) {
	ctx := context.Background()

	// Atomically increment use_count only if the invite is still valid.
	q := dbsqlc.New(WrapDB(s.wdb))
	affected, err := q.AtomicClaimInvite(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("failed to claim invite: %w", err)
	}
	if affected == 0 {
		// No rows updated — either not found, expired, or maxed out.
		// Read to give a better error message.
		invite, err := s.GetByCode(code)
		if err != nil {
			return nil, fmt.Errorf("invite not found")
		}
		now := time.Now().Unix()
		if invite.ExpiresAt != nil && *invite.ExpiresAt < now {
			return nil, fmt.Errorf("invite has expired")
		}
		if invite.MaxUses != nil && invite.UseCount >= *invite.MaxUses {
			return nil, fmt.Errorf("invite has reached its maximum uses")
		}
		return nil, fmt.Errorf("invite not found")
	}

	// Read back the invite (post-increment) for the caller.
	invite, err := s.GetByCode(code)
	if err != nil {
		return nil, fmt.Errorf("invite not found")
	}
	return invite, nil
}

func (s *InviteStore) ListForNetwork(networkID string) ([]NetworkInvite, error) {
	q := dbsqlc.New(WrapDB(s.rdb))
	rows, err := q.ListInvitesForNetwork(context.Background(), networkID)
	if err != nil {
		return nil, err
	}
	invites := make([]NetworkInvite, len(rows))
	for i, r := range rows {
		invites[i] = NetworkInvite{
			ID:        r.ID,
			NetworkID: r.NetworkID,
			CreatedBy: r.CreatedBy,
			Code:      r.Code,
			Role:      r.Role,
			MaxUses:   r.MaxUses,
			UseCount:  r.UseCount,
			ExpiresAt: r.ExpiresAt,
			CreatedAt: r.CreatedAt,
		}
	}
	return invites, nil
}

func (s *InviteStore) Delete(id string) error {
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.DeleteInvite(context.Background(), id)
}

func (s *InviteStore) DeleteExpired() error {
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.DeleteExpiredInvites(context.Background())
}
