package store

import "context"

// DeleteExpiredPendingApps removes pending_github_apps rows whose
// expires_at is at or before nowUnix. Returns how many rows were dropped.
//
// Called periodically by the worker. Stale rows accumulate when users
// abandon the manifest flow halfway through.
func (db *DB) DeleteExpiredPendingApps(ctx context.Context, nowUnix int64) (int64, error) {
	res, err := db.ExecContext(ctx,
		`DELETE FROM pending_github_apps WHERE expires_at <= ? AND expires_at > 0`,
		nowUnix,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
