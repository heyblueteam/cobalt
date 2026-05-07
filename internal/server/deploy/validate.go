package deploy

import (
	"context"
	"fmt"

	"github.com/heyblueteam/cobalt/internal/server/store"
)

func Validate(ctx context.Context, db *store.DB, req EnqueueRequest) error {
	if req.ProjectID == 0 {
		return fmt.Errorf("project_id is required")
	}

	var n int64
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
	n = results[0].Values[0][0].(int64)
	if n == 0 {
		return store.ErrNotFound
	}

	return nil
}
