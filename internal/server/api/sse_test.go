package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSSE_DataEmitsEventLines(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	sse, err := newSSE(rec)
	if err != nil {
		t.Fatalf("newSSE: %v", err)
	}
	if err := sse.data("42", "first\nsecond"); err != nil {
		t.Fatalf("data: %v", err)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "id: 42") {
		t.Errorf("missing id line: %q", out)
	}
	if !strings.Contains(out, "data: first") || !strings.Contains(out, "data: second") {
		t.Errorf("missing data lines: %q", out)
	}
	// Must end with a blank line separating events.
	if !strings.HasSuffix(out, "\n\n") {
		t.Errorf("missing event terminator: %q", out)
	}
}

func TestSSE_HeadersSet(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	if _, err := newSSE(rec); err != nil {
		t.Fatalf("newSSE: %v", err)
	}
	for _, c := range []struct{ k, v string }{
		{"Content-Type", "text/event-stream"},
		{"Cache-Control", "no-cache"},
		{"Connection", "keep-alive"},
		{"X-Accel-Buffering", "no"},
	} {
		if got := rec.Header().Get(c.k); got != c.v {
			t.Errorf("%s: got %q, want %q", c.k, got, c.v)
		}
	}
}

func TestSSE_HeartbeatFormat(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	sse, _ := newSSE(rec)
	if err := sse.heartbeat(); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if !strings.Contains(rec.Body.String(), ": keepalive\n\n") {
		t.Errorf("heartbeat shape: %q", rec.Body.String())
	}
}
