package deploy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// EnqueueRequest is everything the API hands to the deploy queue.
type EnqueueRequest struct {
	ProjectID int64
	CommitSHA string // optional; "" means HEAD of tracked branch
	NoCache   bool
	// CobaltfileOverride is the contents of an inline cobalt.json (the
	// `cobalt deploy --file <path>` flow). Empty means use the cobalt.json
	// shipped in the repo.
	CobaltfileOverride string
}

// Queue is a thin DB-backed enqueue helper. The dispatcher consumes from
// the same DB, not from a channel — the queue persists across daemon
// restarts.
type Queue struct {
	db *store.DB
}

// NewQueue returns a Queue backed by db.
func NewQueue(db *store.DB) *Queue { return &Queue{db: db} }

// Enqueue creates a new deployment row in the queued state. Returns the
// new deployment id and assigned per-project number.
//
// CallersTypically pre-validate (see Validate) before calling this. Enqueue
// itself only enforces basic shape: ProjectID must reference an existing
// project.
func (q *Queue) Enqueue(ctx context.Context, req EnqueueRequest) (id int64, number int, err error) {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("deploy: begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	number, err = nextDeploymentNumberTx(ctx, tx, req.ProjectID)
	if err != nil {
		return 0, 0, err
	}

	res, err := tx.ExecContext(ctx, `
        INSERT INTO deployments (project_id, number, status, commit_sha, no_cache, created_at)
        VALUES (?, ?, ?, ?, ?, unixepoch())
    `,
		req.ProjectID, number, string(cobaltapi.StateQueued),
		nullableString(req.CommitSHA),
		boolToInt(req.NoCache),
	)
	if err != nil {
		return 0, 0, fmt.Errorf("deploy: insert deployment: %w", err)
	}
	id, err = res.LastInsertId()
	if err != nil {
		return 0, 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, 0, err
	}
	return id, number, nil
}

// Cancel marks a deployment as canceled if it's still queued, OR signals
// the dispatcher to abort the in-flight context if it's running.
//
// For queued rows: a single row update.
// For active rows: returns the deployment with cancel-needed=true; the
// caller (Dispatcher.Cancel) is responsible for actually canceling the
// context. Queue does not own goroutines.
func (q *Queue) Cancel(ctx context.Context, deploymentID int64) (cancelInFlight bool, err error) {
	d, err := q.db.GetDeployment(ctx, deploymentID)
	if err != nil {
		return false, err
	}
	switch {
	case d.Status == cobaltapi.StateQueued:
		return false, q.db.SetDeploymentStatus(ctx, deploymentID, cobaltapi.StateCanceled)
	case d.Status.IsActive():
		// Caller cancels the context; the dispatcher's defer block will
		// transition the row to canceled when the runner returns.
		return true, nil
	case d.Status.IsTerminal():
		return false, fmt.Errorf("deploy: cannot cancel %s deployment", d.Status)
	default:
		return false, fmt.Errorf("deploy: unknown status %q", d.Status)
	}
}

// nextDeploymentNumberTx allocates the next per-project deployment number
// inside an open transaction. SQLite's single-writer model means concurrent
// transactions serialize, so this is safe.
func nextDeploymentNumberTx(ctx context.Context, tx *sql.Tx, projectID int64) (int, error) {
	var n sql.NullInt64
	err := tx.QueryRowContext(ctx,
		`SELECT MAX(number) FROM deployments WHERE project_id = ?`,
		projectID,
	).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) || !n.Valid {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	return int(n.Int64) + 1, nil
}

func nullableString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
