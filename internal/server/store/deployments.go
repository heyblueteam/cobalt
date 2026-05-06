package store

import (
	"context"
	"database/sql"
	"strings"
)

// Deployment statuses. The set must stay in sync with the CHECK constraint
// in 0001_init.sql.
const (
	DeployQueued    = "queued"
	DeployFetching  = "fetching"
	DeployBuilding  = "building"
	DeploySwapping  = "swapping"
	DeploySuccess   = "success"
	DeployFailed    = "failed"
	DeployCanceled  = "canceled"
)

// ActiveDeployStatuses is the set of statuses that should keep an image /
// resource alive (i.e., the deployment is in progress or is currently
// serving traffic).
var ActiveDeployStatuses = []string{
	DeployQueued, DeployFetching, DeployBuilding, DeploySwapping, DeploySuccess,
}

// Deployment is a row from the deployments table.
type Deployment struct {
	ID         int64
	ProjectID  int64
	Number     int
	Status     string
	CommitSHA  sql.NullString
	NoCache    bool
	CreatedAt  int64
	StartedAt  sql.NullInt64
	FinishedAt sql.NullInt64
}

// ActiveDeploymentNumbers returns the deployment numbers for a project
// that are still in progress or serving traffic. Used by image cleanup
// to know which image tags must be retained.
func (db *DB) ActiveDeploymentNumbers(ctx context.Context, projectID int64) ([]int, error) {
	placeholders := strings.Repeat("?,", len(ActiveDeployStatuses))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(ActiveDeployStatuses)+1)
	args = append(args, projectID)
	for _, s := range ActiveDeployStatuses {
		args = append(args, s)
	}
	rows, err := db.QueryContext(ctx,
		`SELECT number FROM deployments WHERE project_id = ? AND status IN (`+placeholders+`)`,
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

// CreateDeployment inserts a new deployment row in the queued state. The
// deployment number is supplied by the caller (it's a per-project monotonic
// integer; the simplest source is `MAX(number) + 1` inside a transaction).
func (db *DB) CreateDeployment(ctx context.Context, d Deployment) (int64, error) {
	res, err := db.ExecContext(ctx, `
        INSERT INTO deployments (project_id, number, status, commit_sha, no_cache, created_at)
        VALUES (?, ?, ?, ?, ?, unixepoch())
    `, d.ProjectID, d.Number, d.Status, d.CommitSHA, boolToInt(d.NoCache))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetDeploymentStatus moves a deployment to a new status, optionally
// stamping started_at or finished_at.
func (db *DB) SetDeploymentStatus(ctx context.Context, id int64, status string) error {
	var col string
	switch status {
	case DeployFetching:
		col = "started_at"
	case DeploySuccess, DeployFailed, DeployCanceled:
		col = "finished_at"
	}
	if col == "" {
		_, err := db.ExecContext(ctx, `UPDATE deployments SET status = ? WHERE id = ?`, status, id)
		return err
	}
	_, err := db.ExecContext(ctx,
		`UPDATE deployments SET status = ?, `+col+` = unixepoch() WHERE id = ?`,
		status, id,
	)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
