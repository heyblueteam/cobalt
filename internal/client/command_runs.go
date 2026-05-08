package client

import (
	"context"
	"fmt"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// ListCommandRuns returns the project's `cobalt run` audit log,
// newest first. limit caps the result; pass 0 for the server's
// default (50).
func (c *Client) ListCommandRuns(ctx context.Context, project string, limit int) ([]cobaltapi.CommandRun, error) {
	path := fmt.Sprintf("/api/projects/%s/command-runs", project)
	if limit > 0 {
		path += fmt.Sprintf("?limit=%d", limit)
	}
	var rows []cobaltapi.CommandRun
	if err := c.get(ctx, path, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}
