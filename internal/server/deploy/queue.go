package deploy

import (
	"context"
	"fmt"

	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

type EnqueueRequest struct {
	ProjectID          int64
	CommitSHA          string
	NoCache            bool
	CobaltfileOverride string
}

type Queue struct {
	db *store.DB
}

func NewQueue(db *store.DB) *Queue { return &Queue{db: db} }

func (q *Queue) Enqueue(ctx context.Context, req EnqueueRequest) (id int64, number int, err error) {
	number, err = q.db.NextDeploymentNumber(ctx, req.ProjectID)
	if err != nil {
		return 0, 0, err
	}

	var commitSHA any = nil
	if req.CommitSHA != "" {
		commitSHA = req.CommitSHA
	}
	var cobaltfileOverride any = nil
	if req.CobaltfileOverride != "" {
		cobaltfileOverride = req.CobaltfileOverride
	}

	resp, err := q.db.ExecuteSingle(ctx, `
        INSERT INTO deployments (project_id, number, status, commit_sha, no_cache, cobaltfile_override, created_at)
        VALUES (?, ?, ?, ?, ?, ?, strftime('%s', 'now'))
    `, req.ProjectID, number, string(cobaltapi.StateQueued),
		commitSHA,
		boolToInt(req.NoCache),
		cobaltfileOverride,
	)
	if err != nil {
		return 0, 0, fmt.Errorf("deploy: insert deployment: %w", err)
	}
	if len(resp.Results) == 0 {
		return 0, 0, fmt.Errorf("deploy: no result after insert")
	}
	id = resp.Results[0].LastInsertID
	return id, number, nil
}

func (q *Queue) Cancel(ctx context.Context, deploymentID int64) (cancelInFlight bool, err error) {
	d, err := q.db.GetDeployment(ctx, deploymentID)
	if err != nil {
		return false, err
	}
	switch {
	case d.Status == cobaltapi.StateQueued:
		return false, q.db.SetDeploymentStatus(ctx, deploymentID, cobaltapi.StateCanceled)
	case d.Status.IsActive():
		return true, nil
	case d.Status.IsTerminal():
		return false, fmt.Errorf("%w: cannot cancel %s deployment", ErrDeploymentNotCancelable, d.Status)
	default:
		return false, fmt.Errorf("deploy: unknown status %q", d.Status)
	}
}

func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
