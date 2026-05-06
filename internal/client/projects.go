package client

import (
	"context"
	"fmt"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func (c *Client) ListProjects(ctx context.Context) ([]cobaltapi.Project, error) {
	var projects []cobaltapi.Project
	if err := c.get(ctx, "/api/projects", &projects); err != nil {
		return nil, err
	}
	return projects, nil
}

func (c *Client) GetProject(ctx context.Context, name string) (*cobaltapi.Project, error) {
	var p cobaltapi.Project
	if err := c.get(ctx, fmt.Sprintf("/api/projects/%s", name), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (c *Client) CreateProject(ctx context.Context, req cobaltapi.ProjectCreateRequest) (*cobaltapi.Project, error) {
	var p cobaltapi.Project
	if err := c.post(ctx, "/api/projects", req, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (c *Client) RenameProject(ctx context.Context, name string, req cobaltapi.ProjectRenameRequest) (*cobaltapi.Project, error) {
	var p cobaltapi.Project
	if err := c.patch(ctx, fmt.Sprintf("/api/projects/%s", name), req, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (c *Client) DeleteProject(ctx context.Context, name string) error {
	return c.del(ctx, fmt.Sprintf("/api/projects/%s", name))
}
