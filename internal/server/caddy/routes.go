package caddy

import (
	"context"
	"encoding/json"
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
// Applied as a read-modify-write of the full config (applyCobaltRoutes)
// rather than Caddy's positional `PUT .../routes/0`. The positional PUT is
// a *prepend*, not a replace: re-running it — or re-sending a call that
// timed out after Caddy had already applied it — inserts a duplicate route.
// The atomic /load path drops any existing route carrying this project's
// @id before inserting the new one, so the call is idempotent.
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
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("caddy: marshal project route: %w", err)
	}
	id := ProjectRouteID(projectID)
	return c.applyCobaltRoutes(ctx, func(routes []json.RawMessage) ([]json.RawMessage, error) {
		// Prepend, matching the old routes/0 semantics (project routes sit
		// ahead of the daemon-host route). Every route is host-matched and
		// terminal, so relative order across distinct hosts doesn't change
		// matching.
		out := make([]json.RawMessage, 0, len(routes)+1)
		out = append(out, raw)
		for _, r := range routes {
			if routeID(r) != id {
				out = append(out, r)
			}
		}
		return out, nil
	})
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

// CurrentDomains returns the host list on the project's match block as Caddy
// currently holds it. Implemented as a targeted GET on the hosts @id so we
// don't walk the full config tree (mirrors CurrentUpstream).
//
// Returns nil (not an error) when the project has no route or no host
// matcher yet, so callers treat "absent" as "differs from any desired set".
func (c *Client) CurrentDomains(ctx context.Context, projectID int64) ([]string, error) {
	var hosts []string
	err := c.do(ctx, http.MethodGet, "/id/"+ProjectHostsID(projectID)+"/host", nil, &hosts)
	if err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("caddy: read current domains: %w", err)
	}
	return hosts, nil
}
