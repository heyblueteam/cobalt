package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// stableWebTransaction protects the one durable web service. Numbered siblings
// keep their existing lifecycle. Failed replacements are retained until web has
// recovered its exact previous spec and finished restoring its tasks.
type stableWebTransaction struct {
	name           string
	snapshot       json.RawMessage
	desired        docker.ServiceCreateOpts
	stagePrivate   bool
	incomingStable bool
}

func prepareStableWebTransaction(ctx context.Context, d *docker.Client, p store.Project, dep store.Deployment, cf *cobaltfile.Cobaltfile, built []BuiltService, env map[string]string, previousStable bool) (*stableWebTransaction, error) {
	if !cf.UsesStablePublicWeb() && !previousStable {
		return nil, nil
	}
	tx := &stableWebTransaction{name: docker.StablePublicWebServiceName(p.ID), incomingStable: cf.UsesStablePublicWeb()}
	snapshot, err := d.SnapshotStableService(ctx, tx.name)
	if err != nil {
		return nil, err
	}
	tx.snapshot = snapshot
	tx.desired = docker.ServiceCreateOpts{Name: tx.name, Networks: []docker.NetworkAttachment{{Name: MainNetworkName, Alias: tx.name}}}
	if tx.incomingStable {
		for _, b := range built {
			if b.Name == "web" {
				tx.desired = stablePublicWebServiceOpts(p, dep, b, env)
				break
			}
		}
		web := cf.Services["web"]
		var old struct {
			TaskTemplate struct{ Networks []struct{ Aliases []string } }
		}
		if len(snapshot) > 0 {
			if err := json.Unmarshal(snapshot, &old); err != nil {
				return nil, err
			}
		}
		present := false
		for _, net := range old.TaskTemplate.Networks {
			if slices.Contains(net.Aliases, p.Name+"-web") {
				present = true
			}
		}
		tx.stagePrivate = web.PrivateAddress && !present
	}
	return tx, nil
}

func (tx *stableWebTransaction) stage(opts *docker.ServiceCreateOpts) {
	if tx != nil && tx.stagePrivate {
		opts.Networks = slices.Clone(opts.Networks)
		opts.Networks[0].AdditionalAliases = nil
	}
}

func (tx *stableWebTransaction) activate(ctx context.Context, d *docker.Client) error {
	if tx == nil || tx.incomingStable && !tx.stagePrivate || !tx.incomingStable && tx.snapshot == nil {
		return nil
	}
	return d.ReconcileStableServiceNetworks(ctx, tx.desired)
}

func (tx *stableWebTransaction) restore(ctx context.Context, d *docker.Client) error {
	if tx == nil {
		return nil
	}
	if err := d.RestoreStableService(ctx, tx.name, tx.snapshot); err != nil {
		return fmt.Errorf("restore stable web: %w", err)
	}
	if tx.snapshot != nil {
		return d.WaitForStableServiceSettled(ctx, tx.name)
	}
	return nil
}

func (tx *stableWebTransaction) settle(ctx context.Context, d *docker.Client) error {
	if tx == nil || !tx.incomingStable {
		return nil
	}
	return d.WaitForStableServiceSettled(ctx, tx.name)
}

func stableRecoveryBudget(cf *cobaltfile.Cobaltfile) time.Duration {
	grace := cf.Services["web"].StopGracePeriodSeconds
	return time.Duration(grace)*time.Second + 2*HealthcheckTimeout
}

// A legacy internal-only web may already own the requested private name.
// Reject before hooks or service mutation rather than create a second owner.
func validateStablePrivateAlias(ctx context.Context, d *docker.Client, p store.Project, cf *cobaltfile.Cobaltfile) error {
	internalWeb := cf.Services["web"].ExposedInternally
	if !internalWeb && (!cf.UsesStablePublicWeb() || !cf.Services["web"].PrivateAddress) {
		return nil
	}
	services, err := d.ListServicesForProject(ctx, p.ID)
	if err != nil {
		return err
	}
	for _, service := range services {
		if service.Name == docker.StablePublicWebServiceName(p.ID) {
			if internalWeb {
				return fmt.Errorf("cannot change stable public web directly to exposedInternally; first deploy numbered public web and wait for stable service retirement")
			}
			continue
		}
		if internalWeb {
			continue
		}
		snapshot, err := d.SnapshotStableService(ctx, service.Name)
		if err != nil {
			return err
		}
		var spec struct {
			TaskTemplate struct{ Networks []struct{ Aliases []string } }
		}
		if err := json.Unmarshal(snapshot, &spec); err != nil {
			return err
		}
		for _, network := range spec.TaskTemplate.Networks {
			if slices.Contains(network.Aliases, p.Name+"-web") {
				return fmt.Errorf("privateAddress %q is already owned by %s; retire the internal-only web alias before opting in", p.Name+"-web", service.Name)
			}
		}
	}
	return nil
}

func (tx *stableWebTransaction) recoveryBudget(cf *cobaltfile.Cobaltfile) time.Duration {
	budget := stableRecoveryBudget(cf)
	if tx == nil || tx.snapshot == nil {
		return budget
	}
	var old struct {
		TaskTemplate struct {
			ContainerSpec struct{ StopGracePeriod int64 }
		}
	}
	if json.Unmarshal(tx.snapshot, &old) == nil {
		budget = max(budget, time.Duration(old.TaskTemplate.ContainerSpec.StopGracePeriod)+2*HealthcheckTimeout)
	}
	return budget
}

// Each cutover has one recovery owner. Switching from stable web to exclusive
// writers requires an ordinary numbered-public deployment in between.
func validateStableRecoveryTransition(previousStable bool, cf *cobaltfile.Cobaltfile, built []BuiltService) error {
	if !previousStable {
		return nil
	}
	if publicStopFirstEnabled(cf) {
		return fmt.Errorf("deploy: first deploy numbered public web without stopFirst before enabling public stopFirst; stable and stop-first recovery cannot own the same service")
	}
	for _, b := range built {
		if b.Name != "web" && runsAsService(b.Service) && b.Service.StopFirst {
			return fmt.Errorf("deploy: leave stable web without stopFirst siblings before enabling stopFirst on %q", b.Name)
		}
	}
	return nil
}
