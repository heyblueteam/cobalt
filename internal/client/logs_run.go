package client

import (
	"context"
	"crypto/tls"
	"fmt"
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
	opts := &websocket.DialOptions{
		// WebSocket upgrade relies on hop-by-hop headers (Connection,
		// Upgrade) that HTTP/2 doesn't carry, so we must dial over
		// HTTP/1.1. Setting TLSNextProto to a non-nil empty map is the
		// stdlib's documented opt-out for HTTP/2 negotiation.
		HTTPClient: &http.Client{
			Transport: &http.Transport{
				ForceAttemptHTTP2: false,
				TLSNextProto:      map[string]func(string, *tls.Conn) http.RoundTripper{},
			},
		},
		HTTPHeader: http.Header{
			"Authorization": {"Bearer " + c.server.APIKey},
		},
		Subprotocols: []string{
			cobaltapi.RunSubprotocolV2,
			cobaltapi.RunSubprotocolV1,
		},
	}
	conn, _, err := websocket.Dial(ctx, wsurl, opts)
	if err != nil {
		return nil, "", fmt.Errorf("dial: %w", err)
	}
	return conn, conn.Subprotocol(), nil
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
