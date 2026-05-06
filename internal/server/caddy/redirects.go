package caddy

import (
	"context"
	"net/http"
)

// AddApexWWWRedirect installs a 301 redirect from one host to another. Used
// to transparently fold www.example.com → example.com (or vice versa).
//
// rowID is a stable integer identifier (typically the domains.id of the row
// that owns the redirect) so the redirect can be removed later by id alone.
func (c *Client) AddApexWWWRedirect(ctx context.Context, rowID int64, fromDomain, toDomain string) error {
	body := map[string]any{
		"@id": RedirectID(rowID),
		"handle": []any{
			map[string]any{
				"handler": "subroute",
				"routes": []any{
					map[string]any{
						"handle": []any{
							map[string]any{
								"handler": "static_response",
								"headers": map[string]any{
									"Location": []any{"https://" + toDomain + "{http.request.uri}"},
								},
								"status_code": 301,
							},
						},
					},
				},
			},
		},
		"match":    []any{map[string]any{"host": []any{fromDomain}}},
		"terminal": true,
	}
	return c.do(ctx, http.MethodPut, "/config/apps/http/servers/cobalt/routes/0", body, nil)
}

// RemoveApexWWWRedirect deletes the redirect by its @id.
func (c *Client) RemoveApexWWWRedirect(ctx context.Context, rowID int64) error {
	return c.do(ctx, http.MethodDelete, "/id/"+RedirectID(rowID), nil, nil)
}

// UpdateDaemonHost retargets the daemon's own host matcher. Used when the
// operator runs `cobalt meta host <newdomain>`.
func (c *Client) UpdateDaemonHost(ctx context.Context, host string) error {
	return c.do(ctx, http.MethodPatch, "/id/"+daemonHostID+"/match/0/host/0", host, nil)
}
