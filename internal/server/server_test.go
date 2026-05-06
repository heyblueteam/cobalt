package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func TestHealthz(t *testing.T) {
	t.Parallel()

	srv, addr := startTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}

	var got cobaltapi.Health
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != "ok" {
		t.Errorf("status field: got %q, want %q", got.Status, "ok")
	}
}

// startTestServer binds an httptest.Server using the same mux Run wires up,
// without going through ListenAndServe. It returns the server and its addr.
func startTestServer(t *testing.T) (*http.Server, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, cobaltapi.Health{Status: "ok"})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return &http.Server{}, ts.Listener.Addr().String()
}
