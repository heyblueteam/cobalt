package caddy

import (
	"context"
	"fmt"
	"net/http"
)

// AddApexWWWRedirect installs a 301 redirect from one host to another. Used
// to transparently fold www.example.com → example.com (or vice versa).
//
// rowID is a stable integer identifier (typically the domains.id of the row
// that owns the redirect) so the redirect can be removed later by id alone.
//
// Idempotent: if a route with this @id already exists in Caddy, it's
// deleted first, then re-inserted. Caddy's PUT/POST against a positional
// path like /config/.../routes/0 INSERTS rather than replaces (despite
// what the docs imply), so calling Add twice with the same @id without
// the delete would leave duplicate routes.
func (c *Client) AddApexWWWRedirect(ctx context.Context, rowID int64, fromDomain, toDomain string) error {
	c.adminMu.Lock()
	defer c.adminMu.Unlock()
	return c.addApexWWWRedirectLocked(ctx, rowID, fromDomain, toDomain)
}

func (c *Client) addApexWWWRedirectLocked(ctx context.Context, rowID int64, fromDomain, toDomain string) error {
	// Defensive delete first so re-applying the same @id never
	// accumulates duplicate routes. 404 from Caddy is fine; anything
	// else means Caddy is unreachable or busy and the subsequent
	// insert would hit the same wall, so let that error speak.
	if err := c.removeApexWWWRedirectLocked(ctx, rowID); err != nil && !IsNotFound(err) {
		return err
	}

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
	return c.doLocked(ctx, http.MethodPut, "/config/apps/http/servers/cobalt/routes/0", body, nil)
}

// RemoveApexWWWRedirect deletes the redirect by its @id.
func (c *Client) RemoveApexWWWRedirect(ctx context.Context, rowID int64) error {
	c.adminMu.Lock()
	defer c.adminMu.Unlock()
	return c.removeApexWWWRedirectLocked(ctx, rowID)
}

func (c *Client) removeApexWWWRedirectLocked(ctx context.Context, rowID int64) error {
	err := c.doLocked(ctx, http.MethodDelete, "/id/"+RedirectID(rowID), nil, nil)
	if IsNotFound(err) {
		// Caddy already doesn't have it — that's fine, our DB is the
		// source of truth and the reconciler can leave it dropped.
		return nil
	}
	return err
}

// RedirectSpec is one declarative redirect for SyncProjectRedirects.
// RowID is the domains.id of the row that owns the redirect; FromDomain
// is the host the redirect listens on; ToDomain is the apex it points
// at.
type RedirectSpec struct {
	RowID      int64
	FromDomain string
	ToDomain   string
}

// SyncProjectRedirects converges Caddy's redirect routes for a project
// to exactly the supplied set. existingIDs is the list of redirect
// row ids the daemon currently knows about (i.e. what Caddy might be
// holding under those @ids); want is the desired final set.
//
// Drops anything in existingIDs that isn't in want, then installs each
// want spec via AddApexWWWRedirect (idempotent — Caddy PUT replaces an
// existing @id with the new body). Pass the COMPLETE current set;
// missing ids are treated as "should be removed."
func (c *Client) SyncProjectRedirects(ctx context.Context, existingIDs []int64, want []RedirectSpec) error {
	c.adminMu.Lock()
	defer c.adminMu.Unlock()
	wanted := make(map[int64]bool, len(want))
	for _, r := range want {
		wanted[r.RowID] = true
	}
	for _, id := range existingIDs {
		if wanted[id] {
			continue
		}
		if err := c.removeApexWWWRedirectLocked(ctx, id); err != nil {
			return fmt.Errorf("caddy: drop redirect %d: %w", id, err)
		}
	}
	for _, r := range want {
		if err := c.addApexWWWRedirectLocked(ctx, r.RowID, r.FromDomain, r.ToDomain); err != nil {
			return fmt.Errorf("caddy: install redirect %d (%s→%s): %w",
				r.RowID, r.FromDomain, r.ToDomain, err)
		}
	}
	return nil
}

// UpdateDaemonHost retargets the daemon's own host matcher. Used when the
// operator runs `cobalt meta host <newdomain>`.
func (c *Client) UpdateDaemonHost(ctx context.Context, host string) error {
	return c.do(ctx, http.MethodPatch, "/id/"+daemonHostID+"/match/0/host/0", host, nil)
}
