package client

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/coder/websocket"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func (c *Client) LogsSSE(ctx context.Context, project string, service string) (*http.Response, error) {
	path := fmt.Sprintf("/api/projects/%s/logs", project)
	if service != "" {
		path += "?service=" + service
	}
	return c.StreamGet(ctx, path)
}

// RunWS opens the cobalt-run WebSocket. Subprotocol negotiation:
// the daemon advertises both v2 and v1; we offer v2 first, falling
// back to v1 only if a (very) old server doesn't speak v2. The
// returned subprotocol tells the caller which framer to use.
//
// tty controls whether ?tty=1 is added to the URL — the daemon then
// allocates a real PTY for the container and bridges its master end.
// Only meaningful in v2; v1 servers ignore the flag.
func (c *Client) RunWS(ctx context.Context, project, command, service string, tty bool) (*websocket.Conn, string, error) {
	url := c.baseURL() + fmt.Sprintf("/api/projects/%s/run?command=%s", project, percentEncode(command))
	if service != "" {
		url += "&service=" + percentEncode(service)
	}
	if tty {
		url += "&tty=1"
	}
	wsurl := "ws" + url[4:]
	// WebSocket upgrade relies on hop-by-hop headers (Connection,
	// Upgrade) that HTTP/2 doesn't carry, so we must dial over
	// HTTP/1.1. Setting TLSNextProto to a non-nil empty map is the
	// stdlib's documented opt-out for HTTP/2 negotiation.
	tr := &http.Transport{
		ForceAttemptHTTP2: false,
		TLSNextProto:      map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	// Honor a pinned CA from cliconfig.Server. For daemons installed
	// with `--insecure-tls`, Caddy's local CA isn't in the system pool;
	// without this the WS dial fails with x509 verification even though
	// the regular HTTP client (configured in client.go) succeeds.
	if pemBytes := []byte(c.server.CACertPEM); len(pemBytes) > 0 {
		pool, err := systemPoolPlus(pemBytes)
		if err == nil {
			tr.TLSClientConfig = &tls.Config{RootCAs: pool}
		}
	}
	opts := &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: tr},
		HTTPHeader: http.Header{
			"Authorization": {"Bearer " + c.server.APIKey},
		},
		Subprotocols: []string{
			cobaltapi.RunSubprotocolV2,
			cobaltapi.RunSubprotocolV1,
		},
	}
	conn, resp, err := websocket.Dial(ctx, wsurl, opts)
	if err != nil {
		// On a failed upgrade the daemon's HTTP error body is the
		// useful signal — `project has no successful deployment`,
		// `unauthorized`, etc. Surface that instead of the bare
		// transport error so operators don't see a confusing
		// "expected handshake response status code 101 but got 404"
		// when the real cause is plain.
		if msg := readUpgradeErrorBody(resp); msg != "" {
			return nil, "", errors.New(msg)
		}
		return nil, "", fmt.Errorf("dial: %w", err)
	}
	return conn, conn.Subprotocol(), nil
}

// readUpgradeErrorBody extracts a friendly error message from a
// failed WebSocket upgrade response. Returns "" if the response is
// nil or carries no useful body — caller should fall back to the raw
// dial error in that case.
func readUpgradeErrorBody(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if len(body) == 0 {
		return fmt.Sprintf("%s", resp.Status)
	}
	// Daemon wraps errors in {"error": "..."}. Decode + return the
	// inner string when present; fall back to the raw body otherwise.
	var env struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Error != "" {
		return env.Error
	}
	return string(body)
}

func percentEncode(s string) string {
	allowed := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	result := make([]byte, 0, len(s))
	for _, b := range []byte(s) {
		if containsStr(allowed, b) {
			result = append(result, b)
		} else {
			result = append(result, '%', hexDigit(b>>4), hexDigit(b&0xF))
		}
	}
	return string(result)
}

func containsStr(s string, b byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return true
		}
	}
	return false
}

func hexDigit(b byte) byte {
	b &= 0xF
	if b < 10 {
		return '0' + b
	}
	return 'A' + b - 10
}
