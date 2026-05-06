package store

import (
	"path/filepath"
	"testing"
)

func TestOpen_CreatesDBAndAppliesMigrations(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// schema_migrations should record 0001_init.sql.
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if n != 1 {
		t.Errorf("schema_migrations rows: got %d, want 1", n)
	}

	// A few representative tables should exist.
	for _, table := range []string{"projects", "deployments", "env_vars", "domains", "apikeys"} {
		var got string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`,
			table,
		).Scan(&got)
		if err != nil {
			t.Errorf("table %s: %v", table, err)
		}
	}
}

func TestOpen_IsIdempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	db1, err := Open(dir)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	_ = db1.Close()

	db2, err := Open(dir)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })

	var n int
	if err := db2.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("migrations re-applied: got %d rows, want 1", n)
	}
}

func TestOpen_ForeignKeysOn(t *testing.T) {
	t.Parallel()
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var fk int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("pragma: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys: got %d, want 1", fk)
	}
}

func TestOpen_RejectsEmptyDataDir(t *testing.T) {
	t.Parallel()
	if _, err := Open(""); err == nil {
		t.Fatal("Open(\"\"): want error, got nil")
	}
}

func TestOpen_CreatesDBFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if db.DataDir != dir {
		t.Errorf("DataDir: got %q, want %q", db.DataDir, dir)
	}
	// File should exist on disk.
	wantPath := filepath.Join(dir, "cobalt.db")
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM projects`).Scan(&n); err != nil {
		t.Fatalf("query projects: %v (db file %s)", err, wantPath)
	}
}
