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
	if path == ":memory:" {
		path = "file::memory:?cache=shared"
	}
	dsn := path + "&_journal_mode=WAL&_foreign_keys=on&_busy_timeout=10000" +
		"&_synchronous=NORMAL&_cache_size=-32000&_temp_store=MEMORY"
	if !strings.Contains(path, "?") {
		dsn = path + "?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=10000" +
			"&_synchronous=NORMAL&_cache_size=-32000&_temp_store=MEMORY"
	}

	writeDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open write db: %w", err)
	}
	writeDB.SetMaxOpenConns(1)
	writeDB.SetMaxIdleConns(1)

	readDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		writeDB.Close()
		return nil, fmt.Errorf("open read db: %w", err)
	}
	readDB.SetMaxOpenConns(120)
	readDB.SetMaxIdleConns(10)

	return &DBPair{ReadDB: readDB, WriteDB: writeDB}, nil
}

func Migrate(db *sql.DB) error {
	files, _ := fs.Glob(migrationsFS, "migrations/*.sql")
	sort.Strings(files)

	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at INTEGER NOT NULL DEFAULT (unixepoch())
	)`)

	for _, f := range files {
		version := filepath.Base(f)
		var exists int
		db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&exists)
		if exists > 0 {
			continue
		}
		sqlBytes, _ := migrationsFS.ReadFile(f)
		if _, err := db.Exec(string(sqlBytes)); err != nil {
			return fmt.Errorf("migration %s failed: %w", version, err)
		}
		db.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, version)
		log.Printf("[db] applied migration %s", version)
	}
	return nil
}
