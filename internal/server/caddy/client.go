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
type Client struct {
	http    *http.Client
	baseURL string

	// PlaceholderUpstream is the upstream new project routes start with.
	// Defaults to DefaultPlaceholderUpstream.
	PlaceholderUpstream string
}

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
			Timeout:   10 * time.Second,
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
		client = &http.Client{Timeout: 10 * time.Second}
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
