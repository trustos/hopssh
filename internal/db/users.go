package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/trustos/hopssh/internal/db/dbsqlc"
)

type User struct {
	ID           string
	Email        string
	Name         string
	PasswordHash string
	GitHubID     *string
	CreatedAt    int64
}

// UserProfile is a User without sensitive fields (password hash).
// Used in request contexts and API responses.
type UserProfile struct {
	ID    string
	Email string
	Name  string
}

type UserStore struct {
	rdb *sql.DB
	wdb *sql.DB
}

func NewUserStore(p *DBPair) *UserStore {
	return &UserStore{rdb: p.ReadDB, wdb: p.WriteDB}
}

func (s *UserStore) Create(u *User) error {
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.CreateUser(context.Background(), dbsqlc.CreateUserParams{
		ID:           u.ID,
		Email:        u.Email,
		Name:         u.Name,
		PasswordHash: u.PasswordHash,
		GithubID:     u.GitHubID,
	})
}

// GetProfileByID returns a UserProfile without the password hash.
func (s *UserStore) GetProfileByID(id string) (*UserProfile, error) {
	// Retry on transient SQLite lock errors. QueryRowContext is not retried
	// by the resilience layer, so concurrent requests can fail during writes.
	for attempt := 0; attempt < 3; attempt++ {
		q := dbsqlc.New(WrapDB(s.rdb))
		row, err := q.GetUserProfileByID(context.Background(), id)
		if err == nil {
			return &UserProfile{
				ID:    row.ID,
				Email: row.Email,
				Name:  row.Name,
			}, nil
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if !isLockError(err) {
			return nil, fmt.Errorf("get user profile by id: %w", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil, fmt.Errorf("get user profile by id: database locked after retries")
}

func (s *UserStore) GetByEmail(email string) (*User, error) {
	q := dbsqlc.New(WrapDB(s.rdb))
	row, err := q.GetUserByEmail(context.Background(), email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user by email: %w", err)
	}
	return &User{
		ID:           row.ID,
		Email:        row.Email,
		Name:         row.Name,
		PasswordHash: row.PasswordHash,
		GitHubID:     row.GithubID,
		CreatedAt:    row.CreatedAt,
	}, nil
}

func (s *UserStore) Count() (int, error) {
	q := dbsqlc.New(WrapDB(s.rdb))
	count, err := q.CountUsers(context.Background())
	return int(count), err
}
