package client

import (
	"context"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func (c *Client) ListApps(ctx context.Context) ([]cobaltapi.GithubApp, error) {
	var apps []cobaltapi.GithubApp
	if err := c.get(ctx, "/api/github-apps", &apps); err != nil {
		return nil, err
	}
	return apps, nil
}

func (c *Client) ListRepos(ctx context.Context) ([]cobaltapi.GithubAppRepo, error) {
	var repos []cobaltapi.GithubAppRepo
	if err := c.get(ctx, "/api/github-app-repos", &repos); err != nil {
		return nil, err
	}
	return repos, nil
}

func (c *Client) CreatePendingApp(ctx context.Context, req cobaltapi.PendingAppCreateRequest) (*cobaltapi.PendingAppCreateResponse, error) {
	var resp cobaltapi.PendingAppCreateResponse
	if err := c.post(ctx, "/api/github-apps/create", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) PruneApps(ctx context.Context) (*cobaltapi.PruneResponse, error) {
	var resp cobaltapi.PruneResponse
	if err := c.post(ctx, "/api/github-apps/prune", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) FindAppByOwner(ctx context.Context, owner string) (*cobaltapi.GithubApp, error) {
	apps, err := c.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range apps {
		if a.Owner == owner {
			return &a, nil
		}
	}
	return nil, &APIError{
		Message:    "no GitHub App found for organization \"" + owner + "\"",
		StatusCode: 404,
	}
}
