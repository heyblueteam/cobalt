// Package caddy talks to Caddy's admin API over a unix socket. It does not
// own a desired-state reconciler — every method is a single REST call against
// the live config tree, addressed via Caddy's @id references.
//
// All routes belonging to a project are addressed by the project's stable id
// (not its mutable display name) per the identity/display split described in
// docs/architecture.md. This means renaming a project requires no Caddy work.
package caddy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultSocketPath is the canonical path Caddy listens on for admin requests
// inside the cobalt deployment topology. The daemon and Caddy run as
// separate containers sharing this socket via a volume mount.
const DefaultSocketPath = "/cobalt/caddy-socket/caddy.sock"

// DefaultPlaceholderUpstream is the upstream a brand-new project route points
// at before its first successful deploy. After that, ServeService swaps in
// the real container address.
//
// Convention: the daemon itself listens on this address, so requests to a
// not-yet-deployed project hit a daemon-served placeholder rather than 502.
const DefaultPlaceholderUpstream = "cobalt:80"

// Client is a small wrapper over http.Client targeting Caddy's admin API.
// Methods are safe for concurrent use; the underlying http.Client is.
//
// Concurrency: every PATCH to /id/cobalt-project-handler-* triggers a
// full Caddy config reload, which on a busy server (15+ projects, 30+
// TLS-managed domains) can take 8-12s. Two deploys racing into the
// admin API while a reload is in flight will both timeout against the
// same window. adminMu serializes admin operations on the cobalt side
// so we never pile concurrent PATCHes on top of an in-flight reload.
type Client struct {
	http    *http.Client
	baseURL string

	// adminMu serializes admin requests. Held by every method that
	// mutates Caddy state via /config or /id endpoints, plus the GETs
	// used by the deploy verification path so a verify isn't racing a
	// concurrent PATCH from another goroutine.
	adminMu sync.Mutex

	// PlaceholderUpstream is the upstream new project routes start with.
	// Defaults to DefaultPlaceholderUpstream.
	PlaceholderUpstream string

	// PatchVerifyBackoff is the per-attempt sleep schedule the
	// VerifyServeService loop uses between PATCH and the verifying GET.
	// nil means "use the package default" — production callers should
	// leave this alone; tests shrink it to keep their runtime sane.
	PatchVerifyBackoff []time.Duration

	// StaticSitesDir is the on-disk root for static-deployment files
	// Caddy serves via file_server. Empty means "use the package default"
	// (DefaultStaticSitesDir, /cobalt/srv). Tests override this.
	StaticSitesDir string
}

// DefaultStaticSitesDir is the on-disk root for static-deployment files.
const DefaultStaticSitesDir = "/cobalt/srv"

// NewUnixSocketClient returns a Client that dials Caddy on the given unix
// socket. The HTTP "host" portion of the URL is fixed to "cobalt-caddy" by
// convention and ignored by the transport.
func NewUnixSocketClient(socketPath string) *Client {
	t := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	}
	return &Client{
		http: &http.Client{
			Transport: t,
			Timeout:   60 * time.Second,
		},
		baseURL:             "http://cobalt-caddy",
		PlaceholderUpstream: DefaultPlaceholderUpstream,
	}
}

// NewHTTPClient returns a Client configured to talk to baseURL with a
// supplied http.Client. This is used by tests against httptest.NewServer.
// Production callers want NewUnixSocketClient.
func NewHTTPClient(baseURL string, client *http.Client) *Client {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{
		http:                client,
		baseURL:             strings.TrimRight(baseURL, "/"),
		PlaceholderUpstream: DefaultPlaceholderUpstream,
	}
}

// do issues a request and decodes the response. If body is non-nil it is
// JSON-encoded into the request body. If out is non-nil and the response is
// 2xx, the body is JSON-decoded into out. Non-2xx responses become an error
// containing the upstream status and body (truncated for safety).
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	c.adminMu.Lock()
	defer c.adminMu.Unlock()
	return c.doLocked(ctx, method, path, body, out)
}

// isRetryable reports whether a failed admin call is safe to re-send.
// Retrying is only safe when a call that timed out AFTER Caddy applied it
// converges to the same state on replay:
//
//   - GET is read-only.
//   - PATCH and DELETE are only issued against @id-keyed paths in this
//     package; replaying them converges.
//   - PUT is idempotent ONLY when @id-keyed. Against a positional route
//     index (e.g. `/config/.../routes/0`) it is an INSERT — Caddy prepends
//     rather than replaces — so re-sending a timed-out-but-applied call
//     would duplicate the route. Those surface their error instead.
//   - POST (`/load`) stays outside the retried set; the read-modify-write
//     apply wrapping it lets the failure bubble to its caller.
func isRetryable(method, path string) bool {
	switch method {
	case http.MethodGet, http.MethodPatch, http.MethodDelete:
		return true
	case http.MethodPut:
		return strings.HasPrefix(path, "/id/")
	}
	return false
}

// retryBackoff is the per-attempt sleep schedule for retried admin
// operations. First entry is "no sleep before attempt 0".
var retryBackoff = []time.Duration{0, 2 * time.Second, 8 * time.Second}

// doLocked is the inner request loop, called with adminMu held. The
// retry layer covers transient failures (timeouts, connection refused
// during a Caddy reload) but not permanent errors (4xx responses, JSON
// marshal failures, context cancellation).
func (c *Client) doLocked(ctx context.Context, method, path string, body, out any) error {
	if !isRetryable(method, path) {
		return c.doOnce(ctx, method, path, body, out)
	}
	var lastErr error
	for attempt, backoff := range retryBackoff {
		if attempt > 0 {
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		err := c.doOnce(ctx, method, path, body, out)
		if err == nil {
			return nil
		}
		if !isTransientCaddyError(err) {
			return err
		}
		lastErr = err
	}
	return fmt.Errorf("caddy: %s %s failed after %d attempts: %w",
		method, path, len(retryBackoff), lastErr)
}

// isTransientCaddyError matches the error shapes we expect to recover
// from on a retry: HTTP timeouts and connection refused during a
// reload window. 4xx/5xx response bodies are NOT transient (Caddy is
// telling us the request itself is wrong) — those bubble up.
func isTransientCaddyError(err error) bool {
	if err == nil {
		return false
	}
	// HTTPError = a real response body Caddy returned; not transient.
	var he *HTTPError
	if errors.As(err, &he) {
		return false
	}
	// Anything else is a transport-layer error (timeout, conn refused,
	// EOF mid-read) — Caddy admin is unreachable or busy, retry.
	return true
}

// doOnce performs a single Caddy admin round-trip without retry. Callers
// outside this file should always use do(), which adds locking + retry.
func (c *Client) doOnce(ctx context.Context, method, path string, body, out any) error {
	var buf io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("caddy: marshal: %w", err)
		}
		buf = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, buf)
	if err != nil {
		return fmt.Errorf("caddy: new request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("caddy: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return parseHTTPError(resp)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("caddy: decode response: %w", err)
		}
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	return nil
}

// PingTimeout bounds a single admin liveness probe. Deliberately short: it's
// a health check, not a real operation, and must surface a wedged admin
// socket in seconds rather than inheriting the 60s operation timeout.
const PingTimeout = 5 * time.Second

// Ping reports whether Caddy's admin endpoint is responsive.
//
// It deliberately does NOT take adminMu. When the admin API wedges, an
// in-flight operation can hold adminMu for minutes (60s timeout × retries),
// so a probe that queued behind it could never observe the wedge it exists
// to detect. Ping issues a raw round-trip with its own short deadline.
//
// Any HTTP response — even a 4xx/404 — means the admin API is alive and
// answering; only a transport error (timeout, connection refused, EOF) is a
// failure. The watchdog acts on the latter.
func (c *Client) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, PingTimeout)
	defer cancel()
	err := c.doOnce(ctx, http.MethodGet, "/config/apps/http/servers", nil, nil)
	if err == nil {
		return nil
	}
	var he *HTTPError
	if errors.As(err, &he) {
		return nil
	}
	return err
}

// HTTPError is returned for non-2xx Caddy responses.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("caddy: status %d: %s", e.Status, e.Body)
}

// IsNotFound reports whether err is a 404 from Caddy. Useful for "does this
// @id exist?" checks without an extra HEAD round-trip.
func IsNotFound(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Status == http.StatusNotFound
}

func parseHTTPError(resp *http.Response) error {
	const max = 4 << 10
	body, _ := io.ReadAll(io.LimitReader(resp.Body, max))
	return &HTTPError{Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
}
