// Package store is the daemon's persistence layer. It wraps SQLite, runs
// embedded SQL migrations on Open, and exposes typed CRUD methods for each
// resource. Callers outside the daemon must not import this package.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// DB wraps a *sql.DB plus the data directory it was opened in.
type DB struct {
	*sql.DB
	DataDir string
}

var ErrProjectNameTaken = errors.New("store: project name already in use")

// ErrNotFound is returned by Get* methods when no row matches.
var ErrNotFound = errors.New("store: not found")

// isUniqueConstraint returns true if err is a SQLite UNIQUE constraint
// violation (error code 1555 or message contains "UNIQUE constraint").
func isUniqueConstraint(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint")
}

// Open opens (or creates) the cobalt SQLite database under dataDir, runs any
// pending migrations, and returns a *DB with WAL + busy_timeout configured.
func Open(dataDir string) (*DB, error) {
	if dataDir == "" {
		return nil, errors.New("store.Open: dataDir is required")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("store.Open: create data dir: %w", err)
	}

	dsn := "file:" + filepath.Join(dataDir, "cobalt.db") +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=synchronous(NORMAL)"

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store.Open: %w", err)
	}
	// SQLite is single-writer; one connection avoids "database is locked"
	// surprises while still letting WAL readers proceed in parallel.
	sqlDB.SetMaxOpenConns(1)

	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("store.Open: ping: %w", err)
	}

	db := &DB{DB: sqlDB, DataDir: dataDir}
	if err := db.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}
