package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// Deployment is a row from the deployments table.
type Deployment struct {
	ID                 int64
	ProjectID          int64
	Number             int
	Status             cobaltapi.State
	CommitSHA          sql.NullString
	NoCache            bool
	CobaltfileOverride sql.NullString
	// ResolvedCobaltfile is the cobaltfile that was actually used for the
	// deployment (after merging override or reading from the repo). The
	// Caddy convergence reconciler reads this when computing the desired
	// state for the live deployment.
	ResolvedCobaltfile sql.NullString
	CreatedAt          int64
	StartedAt          sql.NullInt64
	FinishedAt         sql.NullInt64
}

// activeKeepImageStatuses is the set of statuses that keep a deployment's
// image alive for image cleanup. It includes Queued (the deployment hasn't
// started yet but might) plus all active states plus Success (the live
// deployment).
var activeKeepImageStatuses = []cobaltapi.State{
	cobaltapi.StateQueued,
	cobaltapi.StateFetching,
	cobaltapi.StateBuilding,
	cobaltapi.StateSwapping,
	cobaltapi.StateSuccess,
}

// ActiveDeploymentNumbers returns the deployment numbers for a project
// whose images must be retained — anything queued, in flight, or live.
func (db *DB) ActiveDeploymentNumbers(ctx context.Context, projectID int64) ([]int, error) {
	args := make([]any, 0, len(activeKeepImageStatuses)+1)
	args = append(args, projectID)
	for _, s := range activeKeepImageStatuses {
		args = append(args, string(s))
	}
	rows, err := db.QueryContext(ctx,
		`SELECT number FROM deployments WHERE project_id = ? AND status IN (`+
			placeholders(len(activeKeepImageStatuses))+`)`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// CreateDeployment inserts a new deployment row. The number must be the
// next monotonic integer for this project; callers typically use
// NextDeploymentNumber inside a transaction to allocate it.
func (db *DB) CreateDeployment(ctx context.Context, d Deployment) (int64, error) {
	res, err := db.ExecContext(ctx, `
        INSERT INTO deployments (project_id, number, status, commit_sha, no_cache, created_at)
        VALUES (?, ?, ?, ?, ?, unixepoch())
    `, d.ProjectID, d.Number, string(d.Status), d.CommitSHA, boolToInt(d.NoCache))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// NextDeploymentNumber returns max(number)+1 for a project, or 1 if none.
// Run inside a transaction to avoid races with concurrent inserts.
func (db *DB) NextDeploymentNumber(ctx context.Context, projectID int64) (int, error) {
	var n sql.NullInt64
	err := db.QueryRowContext(ctx,
		`SELECT MAX(number) FROM deployments WHERE project_id = ?`,
		projectID,
	).Scan(&n)
	if err != nil {
		return 0, err
	}
	if !n.Valid {
		return 1, nil
	}
	return int(n.Int64) + 1, nil
}

// SetDeploymentStatus moves a deployment to a new status. started_at is
// stamped on the first transition out of Queued; finished_at is stamped on
// any transition into a terminal state.
func (db *DB) SetDeploymentStatus(ctx context.Context, id int64, status cobaltapi.State) error {
	switch {
	case status == cobaltapi.StateFetching:
		_, err := db.ExecContext(ctx,
			`UPDATE deployments SET status = ?, started_at = COALESCE(started_at, unixepoch()) WHERE id = ?`,
			string(status), id,
		)
		return err
	case status.IsTerminal():
		_, err := db.ExecContext(ctx,
			`UPDATE deployments SET status = ?, finished_at = unixepoch() WHERE id = ?`,
			string(status), id,
		)
		return err
	default:
		_, err := db.ExecContext(ctx,
			`UPDATE deployments SET status = ? WHERE id = ?`,
			string(status), id,
		)
		return err
	}
}

// GetDeployment returns a single deployment by id.
func (db *DB) GetDeployment(ctx context.Context, id int64) (*Deployment, error) {
	var d Deployment
	var status string
	err := db.QueryRowContext(ctx, `
        SELECT id, project_id, number, status, commit_sha, no_cache, cobaltfile_override,
               resolved_cobaltfile, created_at, started_at, finished_at
        FROM deployments WHERE id = ?
    `, id).Scan(
		&d.ID, &d.ProjectID, &d.Number, &status, &d.CommitSHA, &d.NoCache,
		&d.CobaltfileOverride, &d.ResolvedCobaltfile,
		&d.CreatedAt, &d.StartedAt, &d.FinishedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.Status = cobaltapi.State(status)
	return &d, nil
}

// QueuedDeployments returns every queued deployment, ordered by
// (project_id, number). Used by the dispatcher to pick the next deploy
// per project.
func (db *DB) QueuedDeployments(ctx context.Context) ([]Deployment, error) {
	rows, err := db.QueryContext(ctx, `
        SELECT id, project_id, number, status, commit_sha, no_cache, cobaltfile_override,
               resolved_cobaltfile, created_at, started_at, finished_at
        FROM deployments
        WHERE status = ?
        ORDER BY project_id, number
    `, string(cobaltapi.StateQueued))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDeployments(rows)
}

// SetResolvedCobaltfile persists the cobaltfile that was used for a
// deployment. The orchestrator calls this after Preparer parses
// cobalt.json (or merges the inline override) so the convergence
// reconciler has authoritative state to read.
func (db *DB) SetResolvedCobaltfile(ctx context.Context, deploymentID int64, raw string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE deployments SET resolved_cobaltfile = ? WHERE id = ?`,
		raw, deploymentID,
	)
	return err
}

// GetLastSuccessfulDeployment returns the most recent successful
// deployment for a project (highest number with status=success). Returns
// ErrNotFound when no prior success exists — used by the deploy flow's
// rollback path, where "no rollback target" means we don't try to revert.
func (db *DB) GetLastSuccessfulDeployment(ctx context.Context, projectID int64) (*Deployment, error) {
	var d Deployment
	var status string
	err := db.QueryRowContext(ctx, `
        SELECT id, project_id, number, status, commit_sha, no_cache, cobaltfile_override,
               resolved_cobaltfile, created_at, started_at, finished_at
        FROM deployments
        WHERE project_id = ? AND status = ?
        ORDER BY number DESC
        LIMIT 1
    `, projectID, string(cobaltapi.StateSuccess)).Scan(
		&d.ID, &d.ProjectID, &d.Number, &status, &d.CommitSHA, &d.NoCache,
		&d.CobaltfileOverride, &d.ResolvedCobaltfile,
		&d.CreatedAt, &d.StartedAt, &d.FinishedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.Status = cobaltapi.State(status)
	return &d, nil
}

// ActiveDeployments returns deployments currently in flight (any active
// state). Used by daemon-restart recovery to mark them failed.
func (db *DB) ActiveDeployments(ctx context.Context) ([]Deployment, error) {
	active := cobaltapi.ActiveStatesList()
	args := make([]any, 0, len(active))
	for _, s := range active {
		args = append(args, string(s))
	}
	rows, err := db.QueryContext(ctx, `
        SELECT id, project_id, number, status, commit_sha, no_cache, cobaltfile_override,
               resolved_cobaltfile, created_at, started_at, finished_at
        FROM deployments WHERE status IN (`+placeholders(len(active))+`)
        ORDER BY project_id, number
    `, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDeployments(rows)
}

func scanDeployments(rows *sql.Rows) ([]Deployment, error) {
	var out []Deployment
	for rows.Next() {
		var d Deployment
		var status string
		if err := rows.Scan(
			&d.ID, &d.ProjectID, &d.Number, &status, &d.CommitSHA, &d.NoCache,
			&d.CobaltfileOverride, &d.ResolvedCobaltfile,
		&d.CreatedAt, &d.StartedAt, &d.FinishedAt,
		); err != nil {
			return nil, err
		}
		d.Status = cobaltapi.State(status)
		out = append(out, d)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
