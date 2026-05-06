package middleware

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/store"
)

func TestRequestID_Generates(t *testing.T) {
	t.Parallel()
	var seen string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestIDFrom(r.Context())
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if seen == "" {
		t.Error("RequestIDFrom: empty inside handler")
	}
	if got := rec.Header().Get("X-Request-ID"); got != seen {
		t.Errorf("response header: got %q, want %q", got, seen)
	}
}

func TestRequestID_PreservesIncoming(t *testing.T) {
	t.Parallel()
	const want = "abc-123"
	var seen string
	h := RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = RequestIDFrom(r.Context())
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-ID", want)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if seen != want {
		t.Errorf("got %q, want %q", seen, want)
	}
}

func TestRecover_TurnsPanicInto500(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := Recover(log)(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500", rec.Code)
	}
}

func TestBearerAuth(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	insertAPIKey(t, db, "secret-key", "test")

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mw := BearerAuth(db.DB, log)

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"valid", "Bearer secret-key", http.StatusOK},
		{"wrong key", "Bearer wrong", http.StatusUnauthorized},
		{"missing", "", http.StatusUnauthorized},
		{"wrong scheme", "Basic secret-key", http.StatusUnauthorized},
		{"empty bearer", "Bearer ", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if APIKeyIDFrom(r.Context()) == 0 {
					t.Error("APIKeyIDFrom: 0 inside authed handler")
				}
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest("GET", "/", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("status: got %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestBearerAuth_UpdatesLastUsedAt(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	id := insertAPIKey(t, db, "k", "test")

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := BearerAuth(db.DB, log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer k")
	h.ServeHTTP(httptest.NewRecorder(), req)

	// last_used_at write is async; poll briefly.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var lu sql.NullInt64
		if err := db.QueryRow(`SELECT last_used_at FROM apikeys WHERE id=?`, id).Scan(&lu); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if lu.Valid {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("last_used_at never updated")
}

func TestLogger_RecordsStatus(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	log := slog.New(slog.NewTextHandler(&sb, nil))
	h := RequestID(Logger(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	out := sb.String()
	for _, want := range []string{"status=418", "method=GET", "path=/x", "request_id="} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q\nfull log: %s", want, out)
		}
	}
}

// helpers

func newTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func insertAPIKey(t *testing.T, db *store.DB, raw, name string) int64 {
	t.Helper()
	res, err := db.ExecContext(context.Background(),
		`INSERT INTO apikeys (key_hash, name, created_at) VALUES (?, ?, unixepoch())`,
		HashAPIKey(raw), name,
	)
	if err != nil {
		t.Fatalf("insert apikey: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}
