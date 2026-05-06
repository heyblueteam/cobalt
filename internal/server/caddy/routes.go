package caddy

import (
	"context"
	"fmt"
	"net/http"
)

// SetDomainsForProject is the high-level entry point used by the API and the
// deployment flow. It reconciles the project's route to point at the supplied
// list of domains, creating, updating, or removing the route as needed.
//
// Empty domains → remove the route entirely.
// New project → create the route with the placeholder upstream.
// Existing project → patch the host matcher in place.
func (c *Client) SetDomainsForProject(ctx context.Context, projectID int64, domains []string) error {
	exists, err := c.ProjectRouteExists(ctx, projectID)
	if err != nil {
		return err
	}
	if len(domains) == 0 {
		if exists {
			return c.RemoveProjectRoute(ctx, projectID)
		}
		return nil
	}
	if exists {
		return c.UpdateProjectDomains(ctx, projectID, domains)
	}
	return c.AddProjectRoute(ctx, projectID, domains)
}

// AddProjectRoute creates the project's top-level route, with the supplied
// domain matchers and the placeholder upstream. The deploy flow later calls
// ServeService to swap in the real container.
//
// Caddy's admin API treats /config/.../routes/0 as "prepend to the routes
// slice" — this is how every disco-style daemon adds new routes today.
func (c *Client) AddProjectRoute(ctx context.Context, projectID int64, domains []string) error {
	body := map[string]any{
		"@id": ProjectRouteID(projectID),
		"handle": []any{
			map[string]any{
				"handler": "subroute",
				"routes": []any{
					map[string]any{
						"handle": []any{
							map[string]any{
								"handler": "encode",
								"encodings": map[string]any{
									"gzip": map[string]any{},
									"zstd": map[string]any{},
								},
							},
							map[string]any{
								"@id":     ProjectHandlerID(projectID),
								"handler": "reverse_proxy",
								"upstreams": []any{
									map[string]any{"dial": c.PlaceholderUpstream},
								},
							},
						},
					},
				},
			},
		},
		"match": []any{
			map[string]any{
				"@id":  ProjectHostsID(projectID),
				"host": domains,
			},
		},
		"terminal": true,
	}
	return c.do(ctx, http.MethodPut, "/config/apps/http/servers/cobalt/routes/0", body, nil)
}

// RemoveProjectRoute deletes the entire project route by its @id.
func (c *Client) RemoveProjectRoute(ctx context.Context, projectID int64) error {
	return c.do(ctx, http.MethodDelete, "/id/"+ProjectRouteID(projectID), nil, nil)
}

// UpdateProjectDomains replaces the host list on the project's match block
// without touching the handler. Used when a domain is added or removed.
func (c *Client) UpdateProjectDomains(ctx context.Context, projectID int64, domains []string) error {
	body := map[string]any{
		"@id":  ProjectHostsID(projectID),
		"host": domains,
	}
	return c.do(ctx, http.MethodPatch, "/id/"+ProjectHostsID(projectID), body, nil)
}

// ProjectRouteExists returns true if the project route is present in Caddy's
// live config. Implemented as a GET against /id/ so we don't have to walk
// the full config tree.
func (c *Client) ProjectRouteExists(ctx context.Context, projectID int64) (bool, error) {
	err := c.do(ctx, http.MethodGet, "/id/"+ProjectRouteID(projectID), nil, nil)
	if err == nil {
		return true, nil
	}
	if IsNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("caddy: project route exists check: %w", err)
}
