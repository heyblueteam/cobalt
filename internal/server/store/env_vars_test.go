package store

import (
	"context"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/encryption"
	rqlitehttp "github.com/rqlite/rqlite-go-http"
)

func newTestCipher(t *testing.T) *encryption.Cipher {
	t.Helper()
	key := make([]byte, encryption.KeyLen)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	c, err := encryption.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func seedProjectForEnvTests(t *testing.T, db *DB) int64 {
	t.Helper()
	pid, err := db.CreateProject(context.Background(), Project{
		Name: "encproj", GithubRepo: "h/r", Branch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func TestSetEnvVar_RoundTripsThroughCipher(t *testing.T) {
	db := openTestDB(t)
	db.SetCipher(newTestCipher(t))
	pid := seedProjectForEnvTests(t, db)

	const plain = "postgres://user:pw@db.internal:5432/app?sslmode=require"
	if err := db.SetEnvVar(context.Background(), pid, "DATABASE_URL", plain); err != nil {
		t.Fatalf("SetEnvVar: %v", err)
	}

	got, err := db.GetEnvVar(context.Background(), pid, "DATABASE_URL")
	if err != nil {
		t.Fatalf("GetEnvVar: %v", err)
	}
	if got.Value != plain {
		t.Errorf("Get: got %q, want %q", got.Value, plain)
	}
}

func TestSetEnvVar_RawRowIsCiphertext(t *testing.T) {
	db := openTestDB(t)
	db.SetCipher(newTestCipher(t))
	pid := seedProjectForEnvTests(t, db)

	const plain = "super-secret-token-123"
	if err := db.SetEnvVar(context.Background(), pid, "TOKEN", plain); err != nil {
		t.Fatalf("SetEnvVar: %v", err)
	}

	// Read straight from rqlite without going through our decryptValue.
	resp, err := db.QuerySingle(context.Background(),
		`SELECT value FROM env_vars WHERE project_id = ? AND key = ?`, pid, "TOKEN")
	if err != nil {
		t.Fatalf("QuerySingle: %v", err)
	}
	results := resp.GetQueryResults()
	if len(results) == 0 || len(results[0].Values) == 0 {
		t.Fatal("row missing")
	}
	stored := blobToString(results[0].Values[0][0])
	if stored == plain {
		t.Errorf("raw rqlite value equals plaintext (%q) — encryption not applied", plain)
	}
	if !encryption.IsCiphertext(stored) {
		t.Errorf("raw rqlite value (%q) doesn't look like a v1 frame", stored)
	}
	if strings.Contains(stored, plain) {
		t.Errorf("raw rqlite value contains plaintext substring")
	}
}

func TestSetEnvVar_NoCipherStoresPlaintext(t *testing.T) {
	db := openTestDB(t)
	// no SetCipher — confirm the fallback path still works for tests
	// and dev installs that haven't generated a key.
	pid := seedProjectForEnvTests(t, db)

	const plain = "plain-value"
	if err := db.SetEnvVar(context.Background(), pid, "FOO", plain); err != nil {
		t.Fatalf("SetEnvVar: %v", err)
	}
	got, err := db.GetEnvVar(context.Background(), pid, "FOO")
	if err != nil {
		t.Fatalf("GetEnvVar: %v", err)
	}
	if got.Value != plain {
		t.Errorf("got %q want %q", got.Value, plain)
	}
}

func TestMigrateEnvVarsToEncrypted_EncryptsExistingPlaintext(t *testing.T) {
	db := openTestDB(t)
	pid := seedProjectForEnvTests(t, db)

	// Seed a plaintext row directly, simulating a daemon upgrade where
	// rqlite already has pre-encryption rows.
	stmt, err := rqlitehttp.NewSQLStatement(
		`INSERT INTO env_vars (project_id, key, value, created_at, updated_at) VALUES (?, ?, ?, strftime('%s','now'), strftime('%s','now'))`,
		pid, "LEGACY", "old-plaintext-value",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Execute(context.Background(), rqlitehttp.SQLStatements{stmt}, nil); err != nil {
		t.Fatal(err)
	}

	// Now wire a cipher and run the migration.
	db.SetCipher(newTestCipher(t))
	n, err := db.MigrateEnvVarsToEncrypted(context.Background())
	if err != nil {
		t.Fatalf("MigrateEnvVarsToEncrypted: %v", err)
	}
	if n != 1 {
		t.Errorf("migrated %d, want 1", n)
	}

	// Subsequent reads decrypt correctly.
	got, err := db.GetEnvVar(context.Background(), pid, "LEGACY")
	if err != nil {
		t.Fatalf("GetEnvVar: %v", err)
	}
	if got.Value != "old-plaintext-value" {
		t.Errorf("decrypted: got %q want %q", got.Value, "old-plaintext-value")
	}

	// Idempotent: a second migration is a no-op.
	n2, err := db.MigrateEnvVarsToEncrypted(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Errorf("second migration migrated %d, want 0 (idempotency)", n2)
	}
}
