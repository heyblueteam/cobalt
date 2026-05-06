package worker

import (
	"context"
	"log/slog"
	"time"
)

// PendingAppCleaner deletes expired rows from pending_github_apps.
// store.DB.DeleteExpiredPendingApps satisfies this.
type PendingAppCleaner interface {
	DeleteExpiredPendingApps(ctx context.Context, nowUnix int64) (int64, error)
}

// CleanupExpiredPendingApps removes any pending GitHub App registration
// state whose expires_at is in the past. Stale rows accumulate when users
// abandon the manifest flow halfway through.
//
// Returns the number of rows deleted.
func CleanupExpiredPendingApps(
	ctx context.Context,
	log *slog.Logger,
	store PendingAppCleaner,
	now time.Time,
) (int64, error) {
	n, err := store.DeleteExpiredPendingApps(ctx, now.Unix())
	if err != nil {
		log.Error("pending_apps cleanup: query failed", "error", err)
		return 0, err
	}
	if n > 0 {
		log.Info("pending_apps cleanup: rows removed", "count", n)
	}
	return n, nil
}
