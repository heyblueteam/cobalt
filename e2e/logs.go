package e2e

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// FetchDeployLog reads the full server-side deploy log for the given
// deployment as a single string. Intended for terminal deployments —
// the server closes the SSE stream after a terminal state, so an EOF
// is the natural stop. Wraps the read in a context so a never-closing
// stream (server bug) fails the test instead of hanging.
//
// Output is returned verbatim — SSE framing (`data: ` prefixes, `id:`
// headers) is preserved. Callers typically grep with strings.Contains
// for sentinel substrings the deploy emits.
func (p *Project) FetchDeployLog(t *testing.T, deploymentID int64, timeout time.Duration) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	resp, err := p.client.DeployOutput(ctx, deploymentID, 0)
	if err != nil {
		t.Fatalf("FetchDeployLog: open stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("FetchDeployLog: status %d for deploy %d", resp.StatusCode, deploymentID)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		t.Fatalf("FetchDeployLog: read stream: %v", err)
	}
	return string(body)
}

// AssertLogContains fails the test if the deploy log does not contain
// every wanted substring. The error names the first missing substring
// and shows a short snippet of the log for triage.
func AssertLogContains(t *testing.T, log string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(log, w) {
			t.Fatalf("deploy log missing %q (got %q)", w, snippet(log))
		}
	}
}

// MustGetBody GETs url and returns the body. Used by scenarios that
// need to inspect a small known-good payload (e.g. a hook-written
// sentinel file served by the web container). Fails the test on any
// non-200 status or transport error.
func MustGetBody(t *testing.T, url string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("GET %s: build req: %v", url, err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatalf("GET %s: read body: %v", url, err)
	}
	return string(b)
}
