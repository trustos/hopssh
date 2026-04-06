package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/trustos/hopssh/internal/db/dbsqlc"
)

type DNSRecord struct {
	ID        string
	NetworkID string
	Name      string // hostname part (e.g., "jellyfin")
	NebulaIP  string // target VPN IP
	CreatedAt int64
}

type DNSRecordStore struct {
	rdb *sql.DB
	wdb *sql.DB
}

func NewDNSRecordStore(p *DBPair) *DNSRecordStore {
	return &DNSRecordStore{rdb: p.ReadDB, wdb: p.WriteDB}
}

func (s *DNSRecordStore) Create(id, networkID, name, nebulaIP string) error {
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.InsertDNSRecord(context.Background(), dbsqlc.InsertDNSRecordParams{
		ID:        id,
		NetworkID: networkID,
		Name:      name,
		NebulaIp:  nebulaIP,
	})
}

func (s *DNSRecordStore) ListForNetwork(networkID string) ([]*DNSRecord, error) {
	q := dbsqlc.New(WrapDB(s.rdb))
	rows, err := q.ListDNSRecordsForNetwork(context.Background(), networkID)
	if err != nil {
		return nil, err
	}
	records := make([]*DNSRecord, 0, len(rows))
	for _, r := range rows {
		records = append(records, &DNSRecord{
			ID:        r.ID,
			NetworkID: r.NetworkID,
			Name:      r.Name,
			NebulaIP:  r.NebulaIp,
			CreatedAt: r.CreatedAt,
		})
	}
	return records, nil
}

func (s *DNSRecordStore) Get(id, networkID string) (*DNSRecord, error) {
	q := dbsqlc.New(WrapDB(s.rdb))
	row, err := q.GetDNSRecord(context.Background(), dbsqlc.GetDNSRecordParams{
		ID:        id,
		NetworkID: networkID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get dns record: %w", err)
	}
	return &DNSRecord{
		ID:        row.ID,
		NetworkID: row.NetworkID,
		Name:      row.Name,
		NebulaIP:  row.NebulaIp,
		CreatedAt: row.CreatedAt,
	}, nil
}

func (s *DNSRecordStore) Delete(id, networkID string) error {
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.DeleteDNSRecord(context.Background(), dbsqlc.DeleteDNSRecordParams{
		ID:        id,
		NetworkID: networkID,
	})
}

func (s *DNSRecordStore) DeleteForNetwork(networkID string) error {
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.DeleteDNSRecordsForNetwork(context.Background(), networkID)
}

func (s *DNSRecordStore) NameExists(networkID, name string) bool {
	q := dbsqlc.New(WrapDB(s.rdb))
	count, err := q.DNSRecordNameExists(context.Background(), dbsqlc.DNSRecordNameExistsParams{
		NetworkID: networkID,
		Name:      name,
	})
	return err == nil && count > 0
}
