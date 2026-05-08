package client

import (
	"context"
	"fmt"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func (c *Client) ListEnvVars(ctx context.Context, project string) ([]cobaltapi.EnvVar, error) {
	var vars []cobaltapi.EnvVar
	if err := c.get(ctx, fmt.Sprintf("/api/projects/%s/env", project), &vars); err != nil {
		return nil, err
	}
	return vars, nil
}

func (c *Client) GetEnvVar(ctx context.Context, project, key string) (*cobaltapi.EnvVar, error) {
	var envVar cobaltapi.EnvVar
	if err := c.get(ctx, fmt.Sprintf("/api/projects/%s/env/%s", project, key), &envVar); err != nil {
		return nil, err
	}
	return &envVar, nil
}

func (c *Client) SetEnvVars(ctx context.Context, project string, req cobaltapi.EnvSetRequest) ([]cobaltapi.EnvVar, error) {
	var vars []cobaltapi.EnvVar
	if err := c.post(ctx, fmt.Sprintf("/api/projects/%s/env", project), req, &vars); err != nil {
		return nil, err
	}
	return vars, nil
}

func (c *Client) DeleteEnvVar(ctx context.Context, project, key string, redeploy bool) error {
	path := fmt.Sprintf("/api/projects/%s/env/%s", project, key)
	if redeploy {
		path += "?redeploy=true"
	}
	return c.del(ctx, path)
}
