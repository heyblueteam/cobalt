package worker

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

type stableWebAliasReconciler interface {
	SnapshotStableService(context.Context, string) (json.RawMessage, error)
	ReconcileStableServiceNetworks(context.Context, docker.ServiceCreateOpts) error
}

// reconcileStableWebAliases runs under the Caddy reconciler's project lock.
// Exact successful-deployment ownership prevents failed cutovers from being
// legitimized by a later periodic networking update.
func reconcileStableWebAliases(ctx context.Context, log *slog.Logger, target any, p store.Project, live store.Deployment, cf *cobaltfile.Cobaltfile) error {
	d, ok := target.(stableWebAliasReconciler)
	if !ok || !cf.UsesStablePublicWeb() {
		return nil
	}
	name := docker.StablePublicWebServiceName(p.ID)
	raw, err := d.SnapshotStableService(ctx, name)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	var spec struct{ Labels map[string]string }
	if err := json.Unmarshal(raw, &spec); err != nil {
		return err
	}
	if spec.Labels[docker.LabelProjectID] != strconv.FormatInt(p.ID, 10) || spec.Labels[docker.LabelServiceName] != "web" || spec.Labels[docker.LabelDeploymentNumber] != strconv.Itoa(live.Number) {
		log.Warn("stable web aliases: ownership differs from successful deployment; skipping", "project_id", p.ID, "service", name, "deployment", live.Number)
		return nil
	}
	network := docker.NetworkAttachment{Name: "cobalt-main", Alias: name}
	if cf.Services["web"].PrivateAddress {
		network.AdditionalAliases = []string{p.Name + "-web"}
	}
	return d.ReconcileStableServiceNetworks(ctx, docker.ServiceCreateOpts{Name: name, ProjectID: p.ID, ProjectName: p.Name, ServiceName: "web", DeploymentNumber: live.Number, Networks: []docker.NetworkAttachment{network}})
}
