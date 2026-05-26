package worker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// CronManager registers project-level cron services with the
// Scheduler and runs them as one-shot containers on schedule.
//
// State: in-memory only. Source of truth is the live deployment's
// resolved cobaltfile per project, joined with the project's
// current env-var state at fire time. Scheduler state is volatile
// so we always re-derive on (a) deploy success and (b) daemon boot.
//
// Single-host assumption holds: this manager fires from one
// daemon process. Multi-host swarm would need leader election to
// avoid duplicate fires.
type CronManager struct {
	sched  *Scheduler
	docker CronDockerRunner
	envs   EnvProvider
	log    *slog.Logger

	mu     sync.Mutex
	byName map[string]cronEntry // scheduler-name → registered entry
}

// cronEntry caches the bits we need to surface via crons-list and
// to run the container. Schedule + Command + ProjectName are also
// what `cobalt crons list` displays.
type cronEntry struct {
	ProjectName      string
	ServiceName      string
	Schedule         string
	Command          string
	Image            string
	DeploymentNumber int
	// Volumes mirrors the cobaltfile-declared volumes for this cron
	// service. We resolve names → cobalt volume names at registration
	// time so each fire mounts the exact same volumes the live
	// service deployment uses.
	Volumes []docker.ServiceVolume
}

// CronDockerRunner is the docker subset CronManager needs. The full
// docker.Client satisfies it; tests substitute a fake.
type CronDockerRunner interface {
	Run(ctx context.Context, opts docker.RunOpts) error
}

// EnvProvider returns the project's current env-var state at fire
// time. *store.DB satisfies it.
type EnvProvider interface {
	EnvVarMap(ctx context.Context, projectID int64) (map[string]string, error)
}

// NewCronManager constructs an empty manager. Boot-time
// reconciliation should be invoked separately via ReconcileAll.
func NewCronManager(sched *Scheduler, dr CronDockerRunner, envs EnvProvider, log *slog.Logger) *CronManager {
	if log == nil {
		log = slog.Default()
	}
	return &CronManager{
		sched:  sched,
		docker: dr,
		envs:   envs,
		log:    log,
		byName: map[string]cronEntry{},
	}
}

// SchedulerName returns the canonical Scheduler entry name for a
// project's cron service. Stable across deploys so Schedule replaces
// in place.
func SchedulerName(projectName, serviceName string) string {
	return "project:" + projectName + ":cron:" + serviceName
}

// schedulerNamePrefix is the prefix every project-cron entry shares.
// Used by RemoveAllForProject to find a project's entries by name.
func schedulerNamePrefix(projectName string) string {
	return "project:" + projectName + ":cron:"
}

// Reconcile makes the scheduler match the cron services in the
// supplied cobaltfile. Adds new ones, replaces those with changed
// schedule/command/image, removes any that are no longer declared.
//
// Called from the orchestrator after every successful cutover so
// the just-deployed cobaltfile becomes the new source of truth.
func (m *CronManager) Reconcile(
	ctx context.Context,
	project store.Project,
	dep store.Deployment,
	cf *cobaltfile.Cobaltfile,
) error {
	if cf == nil {
		return m.RemoveAllForProject(project.Name)
	}

	desired := desiredCrons(project, dep, cf)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Remove entries that are no longer desired.
	for name := range m.byName {
		if !strings.HasPrefix(name, schedulerNamePrefix(project.Name)) {
			continue
		}
		if _, keep := desired[name]; !keep {
			m.sched.Remove(name)
			delete(m.byName, name)
			m.log.Info("cron: unregistered",
				"project", project.Name, "name", name)
		}
	}

	// Schedule replaces in place, so we always pass the current
	// closure regardless of whether the entry existed.
	for name, entry := range desired {
		job := m.cronJob(project, entry)
		if err := m.sched.Schedule(name, entry.Schedule, job); err != nil {
			return fmt.Errorf("cron: schedule %q: %w", name, err)
		}
		m.byName[name] = entry
		m.log.Info("cron: registered",
			"project", project.Name, "service", entry.ServiceName,
			"schedule", entry.Schedule, "deployment", entry.DeploymentNumber)
	}
	return nil
}

// ReconcileAll walks every project, finds its last successful
// deployment, and re-registers any cron services declared in that
// deployment's resolved cobaltfile. Called once at daemon boot
// because scheduler state is volatile.
func (m *CronManager) ReconcileAll(ctx context.Context, projects ProjectLister, deps DeploymentSource) {
	all, err := projects.ListProjects(ctx)
	if err != nil {
		m.log.Warn("cron: boot reconcile: list projects", "error", err)
		return
	}
	for _, p := range all {
		dep, cf, err := deps.LastSuccessfulCobaltfile(ctx, p.ID)
		if err != nil {
			m.log.Debug("cron: boot reconcile: no deployable history",
				"project", p.Name, "error", err)
			continue
		}
		if cf == nil {
			continue
		}
		if err := m.Reconcile(ctx, p, *dep, cf); err != nil {
			m.log.Warn("cron: boot reconcile",
				"project", p.Name, "error", err)
		}
	}
}

// DeploymentSource is the subset of *store.DB ReconcileAll needs.
// Wrapped in an interface so tests can substitute a fake without
// pulling in the whole DB.
type DeploymentSource interface {
	LastSuccessfulCobaltfile(ctx context.Context, projectID int64) (*store.Deployment, *cobaltfile.Cobaltfile, error)
}

// RemoveAllForProject unregisters every cron entry whose name
// belongs to the given project. Called from the API handler before
// project delete so the scheduler doesn't fire crons for a project
// that no longer exists.
func (m *CronManager) RemoveAllForProject(projectName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	prefix := schedulerNamePrefix(projectName)
	for name := range m.byName {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		m.sched.Remove(name)
		delete(m.byName, name)
		m.log.Info("cron: unregistered (project delete)",
			"project", projectName, "name", name)
	}
	return nil
}

// ProjectCronView is a read-only snapshot of one registered cron,
// returned by ListForProject for the API + CLI surface.
type ProjectCronView struct {
	ServiceName      string
	Schedule         string
	Command          string
	DeploymentNumber int
	NextFireAt       time.Time
}

// ListForProject returns every cron currently registered for the
// project, with the scheduler's view of when each next fires. The
// returned slice is sorted by service name.
func (m *CronManager) ListForProject(projectName string) []ProjectCronView {
	m.mu.Lock()
	registered := make(map[string]cronEntry, len(m.byName))
	for k, v := range m.byName {
		if strings.HasPrefix(k, schedulerNamePrefix(projectName)) {
			registered[k] = v
		}
	}
	m.mu.Unlock()

	if len(registered) == 0 {
		return nil
	}

	nextByName := map[string]time.Time{}
	for _, e := range m.sched.Entries() {
		nextByName[e.Name] = e.Next
	}

	out := make([]ProjectCronView, 0, len(registered))
	for name, entry := range registered {
		out = append(out, ProjectCronView{
			ServiceName:      entry.ServiceName,
			Schedule:         entry.Schedule,
			Command:          entry.Command,
			DeploymentNumber: entry.DeploymentNumber,
			NextFireAt:       nextByName[name],
		})
	}
	// sort by service name for stable output
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].ServiceName < out[i].ServiceName {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// cronJob builds the closure scheduler.Schedule will invoke. We
// resolve env vars at fire time (not registration time) so a
// `cobalt env set` between registration and fire is visible to the
// next run, matching `cobalt run`'s "current env" model.
func (m *CronManager) cronJob(project store.Project, entry cronEntry) Job {
	return func(ctx context.Context) {
		log := m.log.With(
			"project", project.Name,
			"service", entry.ServiceName,
			"deployment", entry.DeploymentNumber,
		)
		envVars, err := m.envs.EnvVarMap(ctx, project.ID)
		if err != nil {
			log.Warn("cron: load env vars; firing with empty env", "error", err)
			envVars = map[string]string{}
		}
		// Cron-specific env wins over user-set env if anyone names
		// their var COBALT_PROJECT_NAME etc., which is unlikely but
		// the daemon-injected values are the canonical truth.
		envVars["COBALT_PROJECT_NAME"] = project.Name
		envVars["COBALT_SERVICE_NAME"] = entry.ServiceName
		envVars["COBALT_DEPLOYMENT_NUMBER"] = fmt.Sprintf("%d", entry.DeploymentNumber)
		// Per-fire container name — epoch-suffixed so an overrunning
		// previous run doesn't block this one.
		containerName := docker.CronContainerName(project.Name, entry.ServiceName, time.Now().UnixNano())
		opts := docker.RunOpts{
			ProjectID:        project.ID,
			ProjectName:      project.Name,
			ServiceName:      entry.ServiceName,
			DeploymentNumber: entry.DeploymentNumber,
			ContainerName:    containerName,
			Image:            entry.Image,
			Command:          []string{"sh", "-c", entry.Command},
			EnvVars:          envVars,
			Volumes:          entry.Volumes,
			Networks: []string{
				docker.NetworkName(project.Name, entry.DeploymentNumber),
				"cobalt-main",
			},
			Stdout: io.Discard,
			Stderr: io.Discard,
		}
		start := time.Now()
		if err := m.docker.Run(ctx, opts); err != nil {
			log.Warn("cron: run failed",
				"duration_ms", time.Since(start).Milliseconds(),
				"error", err)
			return
		}
		log.Info("cron: run ok",
			"duration_ms", time.Since(start).Milliseconds())
	}
}

// desiredCrons converts a cobaltfile into the manager's per-name
// view: only `type: cron` services with a non-empty schedule.
func desiredCrons(project store.Project, dep store.Deployment, cf *cobaltfile.Cobaltfile) map[string]cronEntry {
	out := map[string]cronEntry{}
	for serviceName, svc := range cf.Services {
		if svc.Type != cobaltfile.TypeCron {
			continue
		}
		if strings.TrimSpace(svc.Schedule) == "" || strings.TrimSpace(svc.Command) == "" {
			continue
		}
		image := svc.Image
		if image == "" {
			image = "default"
		}
		name := SchedulerName(project.Name, serviceName)
		volumes := make([]docker.ServiceVolume, 0, len(svc.Volumes))
		for _, v := range svc.Volumes {
			volumes = append(volumes, docker.ServiceVolume{
				VolumeName:      docker.VolumeName(project.ID, v.Name),
				DestinationPath: v.DestinationPath,
			})
		}
		out[name] = cronEntry{
			ProjectName:      project.Name,
			ServiceName:      serviceName,
			Schedule:         svc.Schedule,
			Command:          svc.Command,
			Image:            docker.InternalImageName(project.Name, image, dep.Number),
			DeploymentNumber: dep.Number,
			Volumes:          volumes,
		}
	}
	return out
}
