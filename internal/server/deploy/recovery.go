package deploy

import (
	"context"
	"log/slog"

	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// RecoverOnBoot is called once at daemon startup, before the dispatcher
// starts. It reconciles deployments left mid-flight by a previous daemon
// process — they cannot be resumed because cobalt has no way to know what
// docker / Caddy state was reached, so they are marked failed.
//
// Queued rows are left as-is; the dispatcher will pick them up on its
// first sweep.
func RecoverOnBoot(ctx context.Context, db *store.DB, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	active, err := db.ActiveDeployments(ctx)
	if err != nil {
		return err
	}
	for _, d := range active {
		if err := db.SetDeploymentStatus(ctx, d.ID, cobaltapi.StateFailed); err != nil {
			log.Error("recovery: mark failed",
				"deployment_id", d.ID, "previous_status", d.Status, "error", err)
			continue
		}
		log.Warn(
			"recovery: marked failed",
			"deployment_id", d.ID,
			"project_id", d.ProjectID,
			"previous_status", d.Status,
		)
	}
	if len(active) > 0 {
		log.Info("recovery: in-flight deploys reconciled", "count", len(active))
	}
	return nil
}
