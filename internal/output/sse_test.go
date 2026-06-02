package output

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/heyblueteam/cobalt/internal/cliconfig"
	"github.com/heyblueteam/cobalt/internal/client"
)

func TestConsumeSSEReturnsLastID(t *testing.T) {
	stream := "id: 6\ndata: hello\n\nid: 12\ndata: world\n\n"
	var out strings.Builder
	lastID, err := ConsumeSSE(context.Background(), strings.NewReader(stream), &out)
	if err != nil {
		t.Fatalf("ConsumeSSE: %v", err)
	}
	if lastID != "12" {
		t.Errorf("lastID = %q, want %q", lastID, "12")
	}
	if got := out.String(); got != "hello\nworld\n" {
		t.Errorf("out = %q, want %q", got, "hello\nworld\n")
	}
}

// TestFollowDeployOutputReconnects verifies that when the SSE stream ends
// while the deployment is still building, the follower reconnects from the
// last byte offset and keeps streaming until the deploy is terminal —
// rather than quitting mid-build (the "Status: building" bug).
func TestFollowDeployOutputReconnects(t *testing.T) {
	var outputCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/output"):
			n := atomic.AddInt32(&outputCalls, 1)
			w.Header().Set("Content-Type", "text/event-stream")
			switch n {
			case 1:
				// First connection: emit the first chunk (6 bytes) then
				// drop the stream mid-build, with no offset requested.
				if got := r.URL.Query().Get("offset"); got != "" {
					t.Errorf("call 1 offset = %q, want empty", got)
				}
				fmt.Fprint(w, "id: 6\ndata: part1\n\n")
			default:
				// Reconnect: must resume from offset 6.
				if got := r.URL.Query().Get("offset"); got != "6" {
					t.Errorf("call 2 offset = %q, want 6", got)
				}
				fmt.Fprint(w, "id: 12\ndata: part2\n\n")
			}
		default:
			// GET /api/deployments/{id} — building after the first stream,
			// success after the second so the loop terminates.
			status := "building"
			if atomic.LoadInt32(&outputCalls) >= 2 {
				status = "success"
			}
			fmt.Fprintf(w, `{"id":1,"status":%q}`, status)
		}
	}))
	defer srv.Close()

	cl := client.New(cliconfig.Server{Host: srv.URL, APIKey: "test-key"})
	var out strings.Builder
	if err := FollowDeployOutput(context.Background(), cl, 1, 0, &out); err != nil {
		t.Fatalf("FollowDeployOutput: %v", err)
	}
	if got := out.String(); got != "part1\npart2\n" {
		t.Errorf("out = %q, want %q (each chunk exactly once, in order)", got, "part1\npart2\n")
	}
	if n := atomic.LoadInt32(&outputCalls); n != 2 {
		t.Errorf("output stream opened %d times, want 2", n)
	}
}

func TestIsContextCanceled(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated", errors.New("oops"), false},
		{"context canceled", errors.New("Get \"...\": context canceled"), true},
		{
			"deadline exceeded (cancellation alias from net/http)",
			errors.New("read sse stream: context deadline exceeded (Client.Timeout or context cancellation while reading body)"), true,
		},
		{"closed conn", errors.New("read tcp 1.2.3.4: use of closed network connection"), true},
		{"capitalized canceled", errors.New("operation Canceled"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isContextCanceled(tt.err); got != tt.want {
				t.Errorf("isContextCanceled(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
