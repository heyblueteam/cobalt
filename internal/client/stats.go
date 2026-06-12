package client

import (
	"context"
	"net/http"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// ServerStats fetches one host + per-container usage snapshot. Blocks
// ~2s: the daemon gives docker a CPU sampling window.
func (c *Client) ServerStats(ctx context.Context) (cobaltapi.ServerStats, error) {
	var out cobaltapi.ServerStats
	err := c.get(ctx, "/api/server/stats", &out)
	return out, err
}

// ServerStatsSSE opens the live snapshot stream (one ServerStats JSON
// event every ~2s). The caller owns resp.Body.
func (c *Client) ServerStatsSSE(ctx context.Context) (*http.Response, error) {
	return c.StreamGet(ctx, "/api/server/stats?follow=1")
}
