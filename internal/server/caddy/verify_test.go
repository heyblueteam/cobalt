package caddy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fastBackoff is a per-test 5-element schedule that totals ~30ms — fast
// enough for unit tests, long enough for retries to actually sleep.
var fastBackoff = []time.Duration{
	1 * time.Millisecond,
	2 * time.Millisecond,
	4 * time.Millisecond,
	8 * time.Millisecond,
	16 * time.Millisecond,
}

func TestVerifyServeService_FirstAttemptMatches(t *testing.T) {
	t.Parallel()
	f := newFakeCaddy(t)
	c := f.client()
	c.PatchVerifyBackoff = fastBackoff

	if err := c.VerifyServeService(context.Background(), 7, "myapp-7-web", 3000); err != nil {
		t.Errorf("VerifyServeService: %v", err)
	}
}

func TestVerifyServeService_ConvergesAfterRetries(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		// On the first 2 GETs, return a stale upstream. After that,
		// return the new one.
		n := attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n <= 2 {
			_, _ = w.Write([]byte(`"old-upstream"`))
		} else {
			_, _ = w.Write([]byte(`"myapp-7-web"`))
		}
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.URL, srv.Client())
	c.PatchVerifyBackoff = fastBackoff

	if err := c.VerifyServeService(context.Background(), 7, "myapp-7-web", 3000); err != nil {
		t.Errorf("expected eventual convergence, got: %v", err)
	}
}

func TestVerifyServeService_PersistentDriftReturnsError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`"old-upstream"`))
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.URL, srv.Client())
	c.PatchVerifyBackoff = fastBackoff

	err := c.VerifyServeService(context.Background(), 7, "myapp-7-web", 3000)
	var ve *PatchVerifyError
	if !errors.As(err, &ve) {
		t.Fatalf("want *PatchVerifyError, got %T: %v", err, err)
	}
	if ve.Want != "myapp-7-web" {
		t.Errorf("Want: %q", ve.Want)
	}
	if ve.Got != "old-upstream" {
		t.Errorf("Got: %q", ve.Got)
	}
	if ve.ProjectID != 7 {
		t.Errorf("ProjectID: %d", ve.ProjectID)
	}
}

func TestVerifyServeService_PatchFailureBubbles(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.URL, srv.Client())
	c.PatchVerifyBackoff = fastBackoff

	err := c.VerifyServeService(context.Background(), 7, "x", 80)
	if err == nil {
		t.Error("expected error from PATCH failure")
	}
}

func TestVerifyServeService_HonorsContextCancel(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`"wrong"`))
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.URL, srv.Client())
	c.PatchVerifyBackoff = fastBackoff

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.VerifyServeService(ctx, 7, "x", 80)
	if err == nil {
		t.Error("expected context error")
	}
}

func TestVerifyServeService_TransientReadFailure_RetriesAndSucceeds(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// First attempt: connection reset (transient). Second: stale.
		// Third: correct — retry should recover.
		if n == 1 {
			http.Error(w, "connection reset", http.StatusBadGateway)
			return
		}
		if n == 2 {
			_, _ = w.Write([]byte(`"old-upstream"`))
			return
		}
		_, _ = w.Write([]byte(`"myapp-7-web"`))
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.URL, srv.Client())
	c.PatchVerifyBackoff = fastBackoff

	if err := c.VerifyServeService(context.Background(), 7, "myapp-7-web", 3000); err != nil {
		t.Errorf("expected success after transient read failure, got: %v", err)
	}
}
