package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	apiErr, ok := err.(*APIError)
	if !ok {
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
	apiErr, ok := err.(*APIError)
	if !ok {
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
