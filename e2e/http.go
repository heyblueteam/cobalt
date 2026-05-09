package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// httpClient is the shared probe client. Doesn't follow redirects —
// scenarios assert the redirect itself, then dial the target
// separately if needed.
var httpClient = &http.Client{
	Timeout: 15 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// WaitForHTTP polls the URL until it returns the expected status
// code or the timeout expires. Useful immediately after a deploy
// while the cert is being provisioned and Caddy is settling.
func WaitForHTTP(url string, want int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			cancel()
			return err
		}
		resp, err := httpClient.Do(req)
		cancel()
		if err != nil {
			lastErr = err
		} else {
			resp.Body.Close()
			if resp.StatusCode == want {
				return nil
			}
			lastErr = fmt.Errorf("status %d (want %d)", resp.StatusCode, want)
		}
		time.Sleep(3 * time.Second)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timeout")
	}
	return fmt.Errorf("waiting for %s = %d: %w", url, want, lastErr)
}

// AssertRedirect issues a single GET against url and verifies that
// the response is a 3xx with a Location matching wantLocation.
func AssertRedirect(url, wantLocation string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return fmt.Errorf("%s: status %d (want 3xx)", url, resp.StatusCode)
	}
	got := resp.Header.Get("Location")
	if got != wantLocation {
		return fmt.Errorf("%s: Location=%q (want %q)", url, got, wantLocation)
	}
	return nil
}

// AssertBodyContains issues a single GET and verifies the body
// contains the substring. Used as a smoke check that the deploy is
// actually serving the fixture app rather than Caddy's default
// response or a stale container.
func AssertBodyContains(url, want string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: status %d (want 200)", url, resp.StatusCode)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if !strings.Contains(string(body), want) {
		return fmt.Errorf("%s: body missing %q (got %q)", url, want, snippet(string(body)))
	}
	return nil
}

func snippet(s string) string {
	const max = 200
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
