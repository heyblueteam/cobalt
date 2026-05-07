package store

import (
	"context"
	"testing"
)

func TestOpen_URL_Required(t *testing.T) {
	t.Parallel()
	if _, err := Open(""); err == nil {
		t.Fatal("Open(\"\"): want error, got nil")
	}
}

func TestInitSchema_CreatesTables(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	for _, table := range []string{"projects", "deployments", "env_vars", "domains", "apikeys"} {
		resp, err := db.QuerySingle(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name=?", table)
		if err != nil {
			t.Errorf("table %s: query error: %v", table, err)
			continue
		}
		results := resp.GetQueryResults()
		if len(results) == 0 || len(results[0].Values) == 0 {
			t.Errorf("table %s: not found", table)
		}
	}
}

func TestInitSchema_IsIdempotent(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	if err := db.InitSchema(ctx); err != nil {
		t.Fatalf("first InitSchema: %v", err)
	}

	if err := db.InitSchema(ctx); err != nil {
		t.Fatalf("second InitSchema: %v", err)
	}
}

func TestPing(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	if err := db.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}
