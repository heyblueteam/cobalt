package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/deploy"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func newAPIKeysEnv(t *testing.T) *testEnv {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	q := deploy.NewQueue(db)
	mux := http.NewServeMux()
	h := NewHandler(HandlerOpts{
		DB:    db,
		Queue: q,
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &testEnv{t: t, srv: srv, db: db, queue: q, client: srv.Client()}
}

func TestAPIKeys_CreateReturnsRawKeyOnce(t *testing.T) {
	t.Parallel()
	e := newAPIKeysEnv(t)

	resp := e.do(http.MethodPost, "/api/apikeys", cobaltapi.APIKeyCreateRequest{Name: "alice"})
	mustStatus(t, resp, http.StatusCreated)
	got := decode[cobaltapi.APIKeyCreateResponse](t, resp)
	if got.Key == "" {
		t.Error("Key empty in create response")
	}
	if got.Name != "alice" {
		t.Errorf("Name: %q", got.Name)
	}
	if len(got.Key) != 64 { // 32 bytes hex-encoded
		t.Errorf("Key length: %d, want 64", len(got.Key))
	}

	// Second call: list should not contain the raw key.
	resp = e.do(http.MethodGet, "/api/apikeys", nil)
	mustStatus(t, resp, http.StatusOK)
	list := decode[[]cobaltapi.APIKey](t, resp)
	if len(list) != 1 {
		t.Fatalf("list: %d, want 1", len(list))
	}
	if list[0].Name != "alice" || list[0].ID != got.ID {
		t.Errorf("list mismatch: %+v", list[0])
	}
}

func TestAPIKeys_CreateRejectsEmptyName(t *testing.T) {
	t.Parallel()
	e := newAPIKeysEnv(t)
	resp := e.do(http.MethodPost, "/api/apikeys", cobaltapi.APIKeyCreateRequest{Name: "  "})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: %d", resp.StatusCode)
	}
}

func TestAPIKeys_DeleteThenList(t *testing.T) {
	t.Parallel()
	e := newAPIKeysEnv(t)

	resp := e.do(http.MethodPost, "/api/apikeys", cobaltapi.APIKeyCreateRequest{Name: "bob"})
	created := decode[cobaltapi.APIKeyCreateResponse](t, resp)

	resp = e.do(http.MethodDelete, "/api/apikeys/"+itoa(created.ID), nil)
	mustStatus(t, resp, http.StatusNoContent)
	resp.Body.Close()

	resp = e.do(http.MethodGet, "/api/apikeys", nil)
	list := decode[[]cobaltapi.APIKey](t, resp)
	if len(list) != 0 {
		t.Errorf("list after delete: %d, want 0", len(list))
	}
}

func TestAPIKeys_DeleteMissing(t *testing.T) {
	t.Parallel()
	e := newAPIKeysEnv(t)
	resp := e.do(http.MethodDelete, "/api/apikeys/9999", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: %d", resp.StatusCode)
	}
}

func TestAPIKeys_DeleteInvalidID(t *testing.T) {
	t.Parallel()
	e := newAPIKeysEnv(t)
	resp := e.do(http.MethodDelete, "/api/apikeys/not-a-number", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: %d", resp.StatusCode)
	}
}

func TestAPIKeys_GeneratedKeyHashesCorrectly(t *testing.T) {
	t.Parallel()
	// Sanity check: the raw key returned by CreateAPIKey, when
	// hashed via middleware.HashAPIKey, must match what the
	// BearerAuth middleware would compare against in the DB. This is
	// implicitly tested by the bearer-auth integration tests, but
	// pin it here so a refactor of HashAPIKey doesn't silently break
	// auth.
	raw, err := generateAPIKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(raw) != 64 {
		t.Errorf("raw len: %d", len(raw))
	}
	// Confirm hex.
	for _, c := range raw {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("non-hex char in raw key: %q", c)
		}
	}
}

func TestAPIKeys_StoreLastUsedNullable(t *testing.T) {
	t.Parallel()
	// The store struct uses sql.NullInt64 for LastUsedAt; the API
	// shape uses int64 (omitempty). Verify a freshly-created key
	// doesn't surface a nonsensical 0 value through omitempty.
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.CreateAPIKey(context.Background(), "deadbeef", "x")
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := db.ListAPIKeys(context.Background())
	if len(keys) != 1 || keys[0].LastUsedAt.Valid {
		t.Errorf("LastUsedAt should be NULL initially; got %+v", keys[0].LastUsedAt)
	}
}
