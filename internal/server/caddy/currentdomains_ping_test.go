package caddy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestCurrentDomains_ReturnsHostList(t *testing.T) {
	t.Parallel()
	wantPath := "/id/" + ProjectHostsID(7) + "/host"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wantPath {
			t.Errorf("path: got %q, want %q", r.URL.Path, wantPath)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`["api.example.com","www.example.com"]`))
	}))
	defer srv.Close()

	c := NewHTTPClient(srv.URL, srv.Client())
	got, err := c.CurrentDomains(context.Background(), 7)
	if err != nil {
		t.Fatalf("CurrentDomains: %v", err)
	}
	if want := []string{"api.example.com", "www.example.com"}; !reflect.DeepEqual(got, want) {
		t.Errorf("hosts: got %v, want %v", got, want)
	}
}

func TestCurrentDomains_NotFoundReturnsNil(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewHTTPClient(srv.URL, srv.Client())
	got, err := c.CurrentDomains(context.Background(), 7)
	if err != nil {
		t.Fatalf("CurrentDomains on 404 should be nil error, got: %v", err)
	}
	if got != nil {
		t.Errorf("hosts on 404: got %v, want nil", got)
	}
}

func TestPing_AliveOn2xx(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := NewHTTPClient(srv.URL, srv.Client())
	if err := c.Ping(context.Background()); err != nil {
		t.Errorf("Ping on 2xx: %v", err)
	}
}

func TestPing_AliveOn4xx(t *testing.T) {
	t.Parallel()
	// A 404 from the admin API still means it's answering → alive.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewHTTPClient(srv.URL, srv.Client())
	if err := c.Ping(context.Background()); err != nil {
		t.Errorf("Ping on 404 should report alive, got: %v", err)
	}
}

func TestPing_FailsOnTransportError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // connection now refused

	c := NewHTTPClient(url, &http.Client{})
	if err := c.Ping(context.Background()); err == nil {
		t.Error("Ping should return a transport error when the admin endpoint is unreachable")
	}
}
