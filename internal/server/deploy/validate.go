package deploy

import (
	"context"
	"encoding/json"
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
// down, rqlite unreachable) are NOT this function's concern.
func Validate(ctx context.Context, db *store.DB, req EnqueueRequest) error {
	if req.ProjectID <= 0 {
		return ErrProjectIDRequired
	}

	resp, err := db.QuerySingle(ctx,
		`SELECT count(*) FROM projects WHERE id = ?`, req.ProjectID,
	)
	if err != nil {
		return fmt.Errorf("deploy: project lookup: %w", err)
	}
	results := resp.GetQueryResults()
	if len(results) == 0 || len(results[0].Values) == 0 {
		return store.ErrNotFound
	}
	if scanCount(results[0].Values[0][0]) == 0 {
		return store.ErrNotFound
	}

	if req.CobaltfileOverride != "" {
		if _, err := cobaltfile.Parse([]byte(req.CobaltfileOverride)); err != nil {
			return fmt.Errorf("%w: %w", ErrCobaltfileInvalid, err)
		}
	}

	return nil
}

// scanCount turns whatever rqlite returned for a count(*) cell into an
// int64. rqlite's HTTP layer encodes numbers as json.Number, not int64.
func scanCount(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case json.Number:
		i, _ := x.Int64()
		return i
	}
	return 0
}
