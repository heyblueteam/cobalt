package client

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func (c *Client) CreateDeployment(ctx context.Context, project string, req cobaltapi.DeploymentCreateRequest) (*cobaltapi.DeploymentCreateResponse, error) {
	var resp cobaltapi.DeploymentCreateResponse
	if err := c.post(ctx, fmt.Sprintf("/api/projects/%s/deployments", project), req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListProjectCrons returns the cron services currently registered
// for a project, with each one's next-fire time.
func (c *Client) ListProjectCrons(ctx context.Context, project string) ([]cobaltapi.ProjectCron, error) {
	var out []cobaltapi.ProjectCron
	if err := c.get(ctx, fmt.Sprintf("/api/projects/%s/crons", project), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateRollback enqueues a rollback to the previous successful
// deployment (req.To = 0) or to a specific deployment number
// (req.To > 0). Returns the new queued deployment row.
func (c *Client) CreateRollback(ctx context.Context, project string, req cobaltapi.RollbackRequest) (*cobaltapi.Deployment, error) {
	var d cobaltapi.Deployment
	if err := c.post(ctx, fmt.Sprintf("/api/projects/%s/rollback", project), req, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (c *Client) ListDeployments(ctx context.Context, project string, limit int) ([]cobaltapi.Deployment, error) {
	path := fmt.Sprintf("/api/projects/%s/deployments", project)
	if limit > 0 {
		path += fmt.Sprintf("?limit=%d", limit)
	}
	var deps []cobaltapi.Deployment
	if err := c.get(ctx, path, &deps); err != nil {
		return nil, err
	}
	return deps, nil
}

func (c *Client) GetDeployment(ctx context.Context, id int64) (*cobaltapi.Deployment, error) {
	var d cobaltapi.Deployment
	if err := c.get(ctx, fmt.Sprintf("/api/deployments/%d", id), &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (c *Client) CancelDeployment(ctx context.Context, id int64) error {
	return c.post(ctx, fmt.Sprintf("/api/deployments/%d/cancel", id), nil, nil)
}

func (c *Client) StreamGet(ctx context.Context, path string) (*http.Response, error) {
	url := c.baseURL() + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.server.APIKey)
	req.Header.Set("Accept", "text/event-stream")
	// Use the no-timeout streaming client: SSE follows outlive the 30s
	// whole-request Timeout on c.http. Cancellation is via ctx.
	return c.stream.Do(req)
}

func (c *Client) DeployOutputURL(deploymentID int64, offset int64) string {
	path := fmt.Sprintf("/api/deployments/%d/output", deploymentID)
	if offset > 0 {
		path += fmt.Sprintf("?offset=%d", offset)
	}
	return path
}

func (c *Client) DeployOutput(ctx context.Context, deploymentID int64, offset int64) (*http.Response, error) {
	return c.StreamGet(ctx, c.DeployOutputURL(deploymentID, offset))
}

func (c *Client) MostRecentDeployment(ctx context.Context, project string) (*cobaltapi.Deployment, error) {
	deps, err := c.ListDeployments(ctx, project, 1)
	if err != nil {
		return nil, err
	}
	if len(deps) == 0 {
		return nil, fmt.Errorf("no deployments found for project %q", project)
	}
	return &deps[0], nil
}

func (c *Client) MostRecentInFlightDeployment(ctx context.Context, project string) (*cobaltapi.Deployment, error) {
	deps, err := c.ListDeployments(ctx, project, 50)
	if err != nil {
		return nil, err
	}
	for _, d := range deps {
		if d.Status == cobaltapi.StateQueued || d.Status.IsActive() {
			return &d, nil
		}
	}
	return nil, fmt.Errorf("no in-flight deployment found for project %q", project)
}

// DeploymentByNumber resolves the per-project number shown by `deployments
// list` (#957) to the deployment. Numbers are what operators read off the
// list and type back; internal ids are not shown there.
func (c *Client) DeploymentByNumber(ctx context.Context, project string, number int) (*cobaltapi.Deployment, error) {
	deps, err := c.ListDeployments(ctx, project, 500)
	if err != nil {
		return nil, err
	}
	for _, d := range deps {
		if d.Number == number {
			return &d, nil
		}
	}
	return nil, fmt.Errorf("no deployment #%d found for project %q (searched the last %d)", number, project, len(deps))
}

func FormatDeployment(d *cobaltapi.Deployment) string {
	return fmt.Sprintf("#%d (id=%d)", d.Number, d.ID)
}

func FormatDeploymentID(d cobaltapi.Deployment) string {
	return strconv.FormatInt(d.ID, 10)
}
