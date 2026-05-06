package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/deploy"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func TestMetaInfo(t *testing.T) {
	t.Parallel()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mux := http.NewServeMux()
	h := NewHandler(HandlerOpts{
		DB:         db,
		Queue:      deploy.NewQueue(db),
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:    "v1.2.3-test",
		PublicHost: "cobalt.example.com",
	})
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Sleep briefly so uptime is non-zero when we ask.
	time.Sleep(20 * time.Millisecond)

	resp, err := http.Get(srv.URL + "/api/meta/info")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	got := decodeAt[cobaltapi.MetaInfo](t, resp)
	if got.Version != "v1.2.3-test" {
		t.Errorf("Version: %q", got.Version)
	}
	if got.Hostname != "cobalt.example.com" {
		t.Errorf("Hostname: %q", got.Hostname)
	}
	if got.UptimeSecs < 0 {
		t.Errorf("UptimeSecs negative: %d", got.UptimeSecs)
	}
	if got.StartedAt == 0 {
		t.Error("StartedAt zero")
	}
}

// decodeAt is a generic JSON-decode helper. Named to avoid colliding
// with `decode` in api_test.go.
func decodeAt[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	return decode[T](t, resp)
}
