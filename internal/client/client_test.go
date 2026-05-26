package client

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/heyblueteam/cobalt/internal/cliconfig"
)

func TestClientGet(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing auth header")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	c := New(cliconfig.Server{Host: srv.URL, APIKey: "test-key"})
	var resp struct{ Status string }
	if err := c.get(context.Background(), "/healthz", &resp); err != nil {
		t.Fatalf("get: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("status: got %q, want ok", resp.Status)
	}
}

func TestClientPost(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: got %s, want POST", r.Method)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["name"] != "test" {
			t.Errorf("name: got %q, want test", body["name"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":1,"name":"test"}`))
	}))
	defer srv.Close()

	c := New(cliconfig.Server{Host: srv.URL, APIKey: "test-key"})
	var resp struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	body := map[string]string{"name": "test"}
	if err := c.post(context.Background(), "/api/projects", body, &resp); err != nil {
		t.Fatalf("post: %v", err)
	}
	if resp.ID != 1 {
		t.Errorf("id: got %d, want 1", resp.ID)
	}
}

func TestClientAPIError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"project not found"}`))
	}))
	defer srv.Close()

	c := New(cliconfig.Server{Host: srv.URL, APIKey: "test-key"})
	var resp struct{}
	err := c.get(context.Background(), "/api/projects/unknown", &resp)
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("got %T, want *APIError", err)
	}
	if apiErr.Message != "project not found" {
		t.Errorf("message: got %q, want 'project not found'", apiErr.Message)
	}
	if apiErr.StatusCode != 404 {
		t.Errorf("status: got %d, want 404", apiErr.StatusCode)
	}
}

func TestClientAPIErrorNonJSON(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`Internal Server Error`))
	}))
	defer srv.Close()

	c := New(cliconfig.Server{Host: srv.URL, APIKey: "test-key"})
	var resp struct{}
	err := c.get(context.Background(), "/api/test", &resp)
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("got %T, want *APIError", err)
	}
	if apiErr.StatusCode != 500 {
		t.Errorf("status: got %d, want 500", apiErr.StatusCode)
	}
}

func TestClientAuthHeader(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if got != "Bearer key-123" {
			t.Errorf("Authorization: got %q, want Bearer key-123", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(cliconfig.Server{Host: srv.URL, APIKey: "key-123"})
	if err := c.get(context.Background(), "/api/test", nil); err != nil {
		t.Fatalf("get: %v", err)
	}
}

func TestClient_TrustsPinnedCA(t *testing.T) {
	// End-to-end shape: a TLS test server signed by an in-process CA
	// hands the cert + key to httptest. Default client (no CACertPEM)
	// must fail verification; pinning the CA's PEM must succeed.
	caPEM, srv := newTLSServer(t)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")

	t.Run("without pin", func(t *testing.T) {
		c := New(cliconfig.Server{Host: "https://" + host, APIKey: "k"})
		err := c.get(context.Background(), "/anything", nil)
		if err == nil {
			t.Fatal("expected TLS verification failure")
		}
		if !strings.Contains(err.Error(), "x509") && !strings.Contains(err.Error(), "tls") {
			t.Errorf("error %q does not look like a TLS failure", err)
		}
	})

	t.Run("with pin", func(t *testing.T) {
		c := New(cliconfig.Server{
			Host:      "https://" + host,
			APIKey:    "k",
			CACertPEM: caPEM,
		})
		if err := c.get(context.Background(), "/anything", nil); err != nil {
			t.Errorf("pinned CA should accept cert: %v", err)
		}
	})

	t.Run("invalid pem ignored", func(t *testing.T) {
		// Garbage PEM → transportFor returns nil → DefaultTransport →
		// verification fails as in the unpinned case. The intent: a
		// corrupted config file shouldn't silently disable TLS by
		// installing an empty pool that trusts nothing-and-everything.
		c := New(cliconfig.Server{
			Host:      "https://" + host,
			APIKey:    "k",
			CACertPEM: "not a cert",
		})
		if err := c.get(context.Background(), "/anything", nil); err == nil {
			t.Fatal("expected TLS failure with invalid pinned PEM")
		}
	})
}

// newTLSServer spins up an httptest.Server with TLS using a freshly
// minted CA + leaf. Returns the CA's PEM (what the CLI would store in
// CACertPEM) and the server itself.
func newTLSServer(t *testing.T) (string, *httptest.Server) {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	srv.StartTLS()
	// srv.Certificate() returns the auto-generated leaf, which is
	// self-signed — perfect for our purposes: pinning it as a "CA"
	// makes the chain trust succeed.
	certDER := srv.Certificate().Raw
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return string(pemBytes), srv
}

func TestClientURLScheme(t *testing.T) {
	tests := []struct {
		host   string
		expect string
	}{
		{"cobalt.blue.cc", "https://cobalt.blue.cc"},
		{"http://127.0.0.1:18080", "http://127.0.0.1:18080"},
		{"https://cobalt.blue.cc", "https://cobalt.blue.cc"},
		{"", "https://localhost"},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			c := New(cliconfig.Server{Host: tt.host})
			if got := c.baseURL(); got != tt.expect {
				t.Errorf("baseURL: got %q, want %q", got, tt.expect)
			}
		})
	}
}

func TestClientDel(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method: got %s, want DELETE", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := New(cliconfig.Server{Host: srv.URL, APIKey: "test-key"})
	if err := c.del(context.Background(), "/api/projects/test"); err != nil {
		t.Fatalf("del: %v", err)
	}
}

func TestClientPatch(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method: got %s, want PATCH", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":1,"name":"renamed"}`))
	}))
	defer srv.Close()

	c := New(cliconfig.Server{Host: srv.URL, APIKey: "test-key"})
	var resp struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	body := map[string]string{"name": "renamed"}
	if err := c.patch(context.Background(), "/api/projects/test", body, &resp); err != nil {
		t.Fatalf("patch: %v", err)
	}
}

func TestRunWS_SurfacesDaemonErrorBodyOn404(t *testing.T) {
	t.Parallel()
	// Daemon-side stand-in: returns 404 + the same JSON body shape
	// the real daemon uses for "no successful deployment". We are
	// testing only the failed-upgrade path, so we never actually
	// upgrade — we want to confirm the CLI returns the inner
	// "error" field, not the bare transport error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"project has no successful deployment"}`))
	}))
	defer srv.Close()

	c := New(cliconfig.Server{Host: srv.URL, APIKey: "test-key"})
	_, _, err := c.RunWS(context.Background(), "api", "echo hi", "web", false)
	if err == nil {
		t.Fatal("expected error from RunWS")
	}
	if got := err.Error(); got != "project has no successful deployment" {
		t.Errorf("error: got %q, want %q", got, "project has no successful deployment")
	}
}

func TestRunWS_FallsBackToStatusOnEmptyBody(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New(cliconfig.Server{Host: srv.URL, APIKey: "bad"})
	_, _, err := c.RunWS(context.Background(), "api", "echo hi", "web", false)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); !strings.Contains(got, "401") {
		t.Errorf("error %q does not contain 401", got)
	}
}
