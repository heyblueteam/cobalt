package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL is api.github.com. Tests override via NewClient.
const DefaultBaseURL = "https://api.github.com"

// Client wraps http.Client with the GitHub API base URL and a small set of
// helpers. It is stateless: tokens, installation IDs, etc. are passed per
// call. Caching belongs in the caller (typically the daemon's store).
type Client struct {
	http    *http.Client
	baseURL string
}

// NewClient returns a Client targeting api.github.com. If httpClient is nil,
// a default with a 30s timeout is used.
func NewClient(httpClient *http.Client) *Client {
	return NewClientWithBaseURL(DefaultBaseURL, httpClient)
}

// NewClientWithBaseURL is the constructor tests use; it lets a fake server
// stand in for api.github.com.
func NewClientWithBaseURL(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		http:    httpClient,
		baseURL: strings.TrimRight(baseURL, "/"),
	}
}

// auth describes how a request should be authenticated.
type auth int

const (
	authNone auth = iota
	authJWT       // Authorization: Bearer <jwt>; for app-level requests
	authToken     // Authorization: token <installation-token>; for installation-level requests
)

// do issues an authenticated request and decodes the response. credential
// is consumed per the auth mode. Non-2xx responses produce *HTTPError.
func (c *Client) do(ctx context.Context, method, path string, mode auth, credential string, body, out any) error {
	var buf io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("github: marshal: %w", err)
		}
		buf = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, buf)
	if err != nil {
		return fmt.Errorf("github: new request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	switch mode {
	case authJWT:
		req.Header.Set("Authorization", "Bearer "+credential)
	case authToken:
		req.Header.Set("Authorization", "token "+credential)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		const max = 4 << 10
		body, _ := io.ReadAll(io.LimitReader(resp.Body, max))
		return &HTTPError{Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("github: decode: %w", err)
		}
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	return nil
}

// HTTPError carries the status code and (truncated) body of a non-2xx
// GitHub API response.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("github: status %d: %s", e.Status, e.Body) }

// IsStatus is a small helper for callers who want to branch on a specific
// status without import-cycling on http.
func IsStatus(err error, status int) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Status == status
}
