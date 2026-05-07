package deploy

import (
	"context"
	"fmt"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// Validate runs the pre-enqueue checks for a deploy request. The goal is
// to surface obvious mistakes at API time rather than 30 seconds into a
// build:
//
//   - The project must exist.
//   - If a CobaltfileOverride is provided, it must parse and validate.
//   - (TODO 8b) Commit reachability check via GitHub API.
//
// The intent is "fail fast on user error"; transient failures (GitHub
// down, sqlite locked) are NOT this function's concern.
func Validate(ctx context.Context, db *store.DB, req EnqueueRequest) error {
	if req.ProjectID <= 0 {
		return ErrProjectIDRequired
	}

	// Project existence — confirms the API key holder isn't pointing at a
	// deleted project. We use GetProjectByID style by iterating but we
	// already have GetProjectByName; a direct id lookup keeps the code
	// path tight.
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM projects WHERE id = ?`, req.ProjectID,
	).Scan(&n); err != nil {
		return fmt.Errorf("deploy: project lookup: %w", err)
	}
	if n == 0 {
		return store.ErrNotFound
	}

	if req.CobaltfileOverride != "" {
		if _, err := cobaltfile.Parse([]byte(req.CobaltfileOverride)); err != nil {
			return fmt.Errorf("%w: %w", ErrCobaltfileInvalid, err)
		}
	}

	return nil
}
