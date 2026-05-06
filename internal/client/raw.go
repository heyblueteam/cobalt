package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
)

func (c *Client) PostRaw(ctx context.Context, path string, contentType string, body io.Reader) (*http.Response, error) {
	url := c.baseURL() + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.server.APIKey)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return c.http.Do(req)
}

func (c *Client) PostBytes(ctx context.Context, path string, data []byte) (*http.Response, error) {
	return c.PostRaw(ctx, path, "application/octet-stream", bytes.NewReader(data))
}
