package store

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migration represents a single SQL file under migrations/.
type migration struct {
	name string // e.g. "0001_init.sql"
	sql  string
}

// migrate applies every embedded migration whose name has not yet been
// recorded in schema_migrations. Migrations are applied in lexical order.
//
// Each migration runs in its own transaction; if a migration fails, the
// transaction rolls back and Open returns the error.
func (db *DB) migrate() error {
	if _, err := db.Exec(`
        CREATE TABLE IF NOT EXISTS schema_migrations (
            name        TEXT NOT NULL PRIMARY KEY,
            applied_at  INTEGER NOT NULL
        )`); err != nil {
		return fmt.Errorf("store.migrate: create schema_migrations: %w", err)
	}

	migs, err := loadMigrations()
	if err != nil {
		return err
	}

	applied := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("store.migrate: list applied: %w", err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			_ = rows.Close()
			return fmt.Errorf("store.migrate: scan applied: %w", err)
		}
		applied[n] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store.migrate: rows: %w", err)
	}
	_ = rows.Close()

	for _, m := range migs {
		if applied[m.name] {
			continue
		}
		if err := applyMigration(db, m); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(db *DB, m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store.migrate %s: begin: %w", m.name, err)
	}
	if _, err := tx.Exec(m.sql); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store.migrate %s: exec: %w", m.name, err)
	}
	if _, err := tx.Exec(
		`INSERT INTO schema_migrations(name, applied_at) VALUES (?, unixepoch())`,
		m.name,
	); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store.migrate %s: record: %w", m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store.migrate %s: commit: %w", m.name, err)
	}
	return nil
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store.migrate: read embedded migrations: %w", err)
	}
	out := make([]migration, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, err := fs.ReadFile(migrationsFS, "migrations/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("store.migrate: read %s: %w", e.Name(), err)
		}
		out = append(out, migration{name: e.Name(), sql: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}
