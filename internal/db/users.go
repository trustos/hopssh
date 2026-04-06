package db

import (
	"database/sql"
	"fmt"
)

type User struct {
	ID           string
	Email        string
	Name         string
	PasswordHash string
	GitHubID     *string
	CreatedAt    int64
}

type UserStore struct {
	rdb *sql.DB
	wdb *sql.DB
}

func NewUserStore(p *DBPair) *UserStore {
	return &UserStore{rdb: p.ReadDB, wdb: p.WriteDB}
}

func (s *UserStore) Create(u *User) error {
	_, err := s.wdb.Exec(`
		INSERT INTO users (id, email, name, password_hash, github_id)
		VALUES (?, ?, ?, ?, ?)
	`, u.ID, u.Email, u.Name, u.PasswordHash, u.GitHubID)
	return err
}

func (s *UserStore) GetByID(id string) (*User, error) {
	var u User
	err := s.rdb.QueryRow(`
		SELECT id, email, name, password_hash, github_id, created_at FROM users WHERE id = ?
	`, id).Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.GitHubID, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	return &u, nil
}

func (s *UserStore) GetByEmail(email string) (*User, error) {
	var u User
	err := s.rdb.QueryRow(`
		SELECT id, email, name, password_hash, github_id, created_at FROM users WHERE email = ?
	`, email).Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.GitHubID, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user by email: %w", err)
	}
	return &u, nil
}

func (s *UserStore) Count() (int, error) {
	var count int
	err := s.rdb.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count)
	return count, err
}
