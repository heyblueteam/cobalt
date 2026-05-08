package store

import (
	"context"
	"errors"
)

// Domain is the full shape of one row in the domains table. Name is
// always set; RedirectTo is non-nil only for redirect rows (the host
// 301s to RedirectTo, which must be another primary domain on the
// same project).
type Domain struct {
	ID         int64
	Name       string
	RedirectTo *string
	CreatedAt  int64
}

// IsRedirect reports whether this domain is a 301 redirect to another
// host (rather than a primary that serves the project's web service).
func (d Domain) IsRedirect() bool {
	return d.RedirectTo != nil && *d.RedirectTo != ""
}

// ListDomainsForProject returns just the host names in insertion
// order. Used by Caddy reconciliation paths that don't care about
// redirect status.
func (db *DB) ListDomainsForProject(ctx context.Context, projectID int64) ([]string, error) {
	sql := `SELECT name FROM domains WHERE project_id = ? ORDER BY id`
	resp, err := db.QuerySingle(ctx, sql, projectID)
	if err != nil {
		return nil, err
	}

	var out []string
	results := resp.GetQueryResults()
	if len(results) == 0 {
		return out, nil
	}
	for _, row := range results[0].Values {
		if len(row) > 0 {
			out = append(out, toString(row[0]))
		}
	}
	return out, nil
}

// ListPrimaryDomainsForProject returns just the host names of the
// project's primary (non-redirect) domains. Used by paths that should
// not include redirect aliases — e.g. when computing the set of hosts
// the project's web service is reachable as for healthcheck purposes.
func (db *DB) ListPrimaryDomainsForProject(ctx context.Context, projectID int64) ([]string, error) {
	sql := `SELECT name FROM domains WHERE project_id = ? AND (redirect_to IS NULL OR redirect_to = '') ORDER BY id`
	resp, err := db.QuerySingle(ctx, sql, projectID)
	if err != nil {
		return nil, err
	}

	var out []string
	results := resp.GetQueryResults()
	if len(results) == 0 {
		return out, nil
	}
	for _, row := range results[0].Values {
		if len(row) > 0 {
			out = append(out, toString(row[0]))
		}
	}
	return out, nil
}

// ListDomainsFullForProject returns every domain row for a project,
// in insertion order, with the full schema (id, name, redirect_to,
// created_at). Used by the API list handler + Caddy reconciliation.
func (db *DB) ListDomainsFullForProject(ctx context.Context, projectID int64) ([]Domain, error) {
	sql := `SELECT id, name, redirect_to, created_at FROM domains WHERE project_id = ? ORDER BY id`
	resp, err := db.QuerySingle(ctx, sql, projectID)
	if err != nil {
		return nil, err
	}
	var out []Domain
	results := resp.GetQueryResults()
	if len(results) == 0 {
		return out, nil
	}
	for _, row := range results[0].Values {
		d := Domain{
			ID:        toInt64(row[0]),
			Name:      toString(row[1]),
			CreatedAt: toInt64(row[3]),
		}
		if row[2] != nil {
			s := toString(row[2])
			if s != "" {
				d.RedirectTo = &s
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// AddDomain inserts a primary domain for a project. RedirectTo is nil.
func (db *DB) AddDomain(ctx context.Context, projectID int64, name string) error {
	return db.addDomainRow(ctx, projectID, name, nil)
}

// AddDomainRedirect inserts a redirect domain. The redirect target
// MUST be an existing primary domain on the same project; the API
// layer is responsible for validating this before calling.
func (db *DB) AddDomainRedirect(ctx context.Context, projectID int64, name, redirectTo string) error {
	return db.addDomainRow(ctx, projectID, name, &redirectTo)
}

func (db *DB) addDomainRow(ctx context.Context, projectID int64, name string, redirectTo *string) error {
	sql := `INSERT INTO domains (project_id, name, redirect_to, created_at) VALUES (?, ?, ?, strftime('%s', 'now'))`
	var rt any
	if redirectTo != nil {
		rt = *redirectTo
	}
	resp, err := db.ExecuteSingle(ctx, sql, projectID, name, rt)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return ErrDomainTaken
		}
		return err
	}
	// rqlite reports SQL constraint failures inside the result, not as
	// a transport error; without this check, a duplicate-domain insert
	// silently no-ops.
	if len(resp.Results) > 0 && resp.Results[0].Error != "" {
		if isUniqueConstraintErr(errors.New(resp.Results[0].Error)) {
			return ErrDomainTaken
		}
		return errors.New(resp.Results[0].Error)
	}
	return nil
}

// RemoveDomain deletes a domain row. If the row is a primary that has
// redirect rows pointing at it, the API caller is responsible for
// removing those first (or accepting they'll become dangling Caddy
// routes the next reconcile catches). The store-level call doesn't
// cascade because we want callers to make the policy decision.
func (db *DB) RemoveDomain(ctx context.Context, projectID int64, name string) error {
	sql := `DELETE FROM domains WHERE project_id = ? AND name = ?`
	resp, err := db.ExecuteSingle(ctx, sql, projectID, name)
	if err != nil {
		return err
	}
	if len(resp.Results) == 0 || resp.Results[0].RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// RemoveDomainAndRedirects deletes a primary plus every redirect that
// targets it. Used when an operator removes a primary that has a
// www→apex (or apex→www) redirect attached.
//
// Returns the redirect-row ids that were cascaded so the caller can
// pass them to the Caddy reconciler — the store-side delete forgets
// the rows, but Caddy still holds routes keyed by those ids that need
// to be torn down explicitly.
func (db *DB) RemoveDomainAndRedirects(ctx context.Context, projectID int64, name string) (removedRedirectIDs []int64, err error) {
	// Look up the redirect ids first so we can return them to the
	// caller for Caddy cleanup. Doing this before the DELETE means we
	// don't lose the ids even if the cascade succeeds.
	cascadedIDs, err := db.redirectIDsTargeting(ctx, projectID, name)
	if err != nil {
		return nil, err
	}
	resp, err := db.ExecuteSingle(ctx,
		`DELETE FROM domains WHERE project_id = ? AND (name = ? OR redirect_to = ?)`,
		projectID, name, name)
	if err != nil {
		return nil, err
	}
	if len(resp.Results) == 0 || resp.Results[0].RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return cascadedIDs, nil
}

// redirectIDsTargeting returns the domains.id of every redirect row
// that points at the given primary host. Used by the cascade path.
func (db *DB) redirectIDsTargeting(ctx context.Context, projectID int64, target string) ([]int64, error) {
	resp, err := db.QuerySingle(ctx,
		`SELECT id FROM domains WHERE project_id = ? AND redirect_to = ?`,
		projectID, target)
	if err != nil {
		return nil, err
	}
	if hasError, _, errMsg := resp.HasError(); hasError {
		return nil, errors.New(errMsg)
	}
	results := resp.GetQueryResults()
	if len(results) == 0 {
		return nil, nil
	}
	out := make([]int64, 0, len(results[0].Values))
	for _, row := range results[0].Values {
		out = append(out, toInt64(row[0]))
	}
	return out, nil
}
