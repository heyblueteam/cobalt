package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/heyblueteam/cobalt/internal/cliconfig"
)

type Client struct {
	server cliconfig.Server
	http   *http.Client
}

type APIError struct {
	Message    string
	StatusCode int
}

func (e *APIError) Error() string { return e.Message }

func New(s cliconfig.Server) *Client {
	return &Client{
		server: s,
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transportFor(s),
		},
	}
}

// transportFor returns a transport that trusts s.CACertPEM in addition
// to the system roots, when CACertPEM is non-empty. When empty, returns
// nil so the http.Client falls back to DefaultTransport — matches the
// behavior for daemons with a publicly-trusted cert.
//
// Pinned-CA trust (vs. InsecureSkipVerify) preserves real chain
// verification: a tampered cert from a MITM still fails because it
// won't chain to the pinned root.
func transportFor(s cliconfig.Server) http.RoundTripper {
	if s.CACertPEM == "" {
		return nil
	}
	pool, err := systemPoolPlus([]byte(s.CACertPEM))
	if err != nil {
		return nil
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool}
	return tr
}

// systemPoolPlus returns the system CA pool with extra PEM appended.
// Used by both the regular HTTP client and the WebSocket dial path so
// they share one definition of "what to trust".
func systemPoolPlus(extraPEM []byte) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(extraPEM) {
		return nil, fmt.Errorf("client: no valid PEM blocks in supplied CA cert")
	}
	return pool, nil
}

func (c *Client) baseURL() string {
	h := c.server.Host
	if h == "" {
		h = "localhost"
	}
	if strings.HasPrefix(h, "http://") || strings.HasPrefix(h, "https://") {
		return h
	}
	return "https://" + h
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

func (c *Client) patch(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPatch, path, body, out)
}

func (c *Client) del(ctx context.Context, path string) error {
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	url := c.baseURL() + path

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.server.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.parseError(resp)
	}

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

func (c *Client) parseError(resp *http.Response) error {
	respBody, _ := io.ReadAll(resp.Body)
	var apiResp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil || apiResp.Error == "" {
		return &APIError{
			Message:    fmt.Sprintf("%s: %s", resp.Status, strings.TrimSpace(string(respBody))),
			StatusCode: resp.StatusCode,
		}
	}
	return &APIError{
		Message:    apiResp.Error,
		StatusCode: resp.StatusCode,
	}
}
