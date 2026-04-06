package db

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DBPair holds separate connection pools for reads and writes.
// SQLite allows concurrent readers with WAL mode, but only one writer at a time.
type DBPair struct {
	ReadDB  *sql.DB
	WriteDB *sql.DB
}

func (p *DBPair) Close() error {
	rerr := p.ReadDB.Close()
	werr := p.WriteDB.Close()
	if werr != nil {
		return werr
	}
	return rerr
}

// Open creates a dual read/write connection pool for SQLite.
func Open(path string) (*DBPair, error) {
	// busy_timeout MUST come before journal_mode=WAL (connection must block
	// on busy before WAL mode is set, per PocketBase's findings).
	pragmas := "_busy_timeout=10000&_journal_mode=WAL&_foreign_keys=on" +
		"&_synchronous=NORMAL&_cache_size=-32000&_temp_store=MEMORY" +
		"&_journal_size_limit=200000000" // 200MB WAL file cap
	var dsn string
	switch {
	case path == ":memory:":
		dsn = "file::memory:?cache=shared&" + pragmas
	case strings.Contains(path, "?"):
		dsn = path + "&" + pragmas
	default:
		dsn = path + "?" + pragmas
	}

	writeDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open write db: %w", err)
	}
	writeDB.SetMaxOpenConns(1)
	writeDB.SetMaxIdleConns(1)
	writeDB.SetConnMaxIdleTime(3 * time.Minute)

	readDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		writeDB.Close()
		return nil, fmt.Errorf("open read db: %w", err)
	}
	readDB.SetMaxOpenConns(20)
	readDB.SetMaxIdleConns(5)
	readDB.SetConnMaxIdleTime(3 * time.Minute)

	return &DBPair{ReadDB: readDB, WriteDB: writeDB}, nil
}

func Migrate(db *sql.DB) error {
	files, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("glob migrations: %w", err)
	}
	sort.Strings(files)

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at INTEGER NOT NULL DEFAULT (unixepoch())
	)`); err != nil {
		return fmt.Errorf("create schema_migrations table: %w", err)
	}

	for _, f := range files {
		version := filepath.Base(f)
		var exists int
		if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %s: %w", version, err)
		}
		if exists > 0 {
			continue
		}
		sqlBytes, err := migrationsFS.ReadFile(f)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", version, err)
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin transaction for %s: %w", version, err)
		}
		if _, err := tx.Exec(string(sqlBytes)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s failed: %w", version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, version); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", version, err)
		}
		log.Printf("[db] applied migration %s", version)
	}
	return nil
}
