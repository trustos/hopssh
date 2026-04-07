package db

import (
	"context"
	"database/sql"

	"github.com/trustos/hopssh/internal/db/dbsqlc"
)

// NetworkMember represents a user's membership in a network.
type NetworkMember struct {
	ID        string
	NetworkID string
	UserID    string
	Role      string
	CreatedAt int64
}

// NetworkMemberWithUser includes user profile info for listing.
type NetworkMemberWithUser struct {
	NetworkMember
	Email string
	Name  string
}

type NetworkMemberStore struct {
	rdb *sql.DB
	wdb *sql.DB
}

func NewNetworkMemberStore(d *DBPair) *NetworkMemberStore {
	return &NetworkMemberStore{rdb: d.ReadDB, wdb: d.WriteDB}
}

func (s *NetworkMemberStore) Add(id, networkID, userID, role string) error {
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.InsertNetworkMember(context.Background(), dbsqlc.InsertNetworkMemberParams{
		ID:        id,
		NetworkID: networkID,
		UserID:    userID,
		Role:      role,
	})
}

func (s *NetworkMemberStore) GetMembership(networkID, userID string) (*NetworkMember, error) {
	q := dbsqlc.New(WrapDB(s.rdb))
	row, err := q.GetNetworkMember(context.Background(), dbsqlc.GetNetworkMemberParams{
		NetworkID: networkID,
		UserID:    userID,
	})
	if err != nil {
		return nil, err
	}
	return &NetworkMember{
		ID:        row.ID,
		NetworkID: row.NetworkID,
		UserID:    row.UserID,
		Role:      row.Role,
		CreatedAt: row.CreatedAt,
	}, nil
}

func (s *NetworkMemberStore) ListForNetwork(networkID string) ([]NetworkMemberWithUser, error) {
	q := dbsqlc.New(WrapDB(s.rdb))
	rows, err := q.ListMembersForNetwork(context.Background(), networkID)
	if err != nil {
		return nil, err
	}
	members := make([]NetworkMemberWithUser, len(rows))
	for i, r := range rows {
		members[i] = NetworkMemberWithUser{
			NetworkMember: NetworkMember{
				ID:        r.ID,
				NetworkID: r.NetworkID,
				UserID:    r.UserID,
				Role:      r.Role,
				CreatedAt: r.CreatedAt,
			},
			Email: r.Email,
			Name:  r.Name,
		}
	}
	return members, nil
}

func (s *NetworkMemberStore) ListNetworkIDsForUser(userID string) ([]string, error) {
	q := dbsqlc.New(WrapDB(s.rdb))
	return q.ListNetworkIDsForMember(context.Background(), userID)
}

func (s *NetworkMemberStore) Remove(id string) error {
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.DeleteNetworkMember(context.Background(), id)
}

func (s *NetworkMemberStore) RemoveByNetworkAndUser(networkID, userID string) error {
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.DeleteNetworkMemberByNetworkAndUser(context.Background(), dbsqlc.DeleteNetworkMemberByNetworkAndUserParams{
		NetworkID: networkID,
		UserID:    userID,
	})
}
