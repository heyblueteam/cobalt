package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

type fakeCronDocker struct {
	mu      sync.Mutex
	runs    []docker.RunOpts
	runErr  error
}

func (f *fakeCronDocker) Run(_ context.Context, opts docker.RunOpts) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, opts)
	return f.runErr
}

type fakeEnvProvider struct {
	envByProject map[int64]map[string]string
	err          error
}

func (f *fakeEnvProvider) EnvVarMap(_ context.Context, projectID int64) (map[string]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[string]string, len(f.envByProject[projectID]))
	for k, v := range f.envByProject[projectID] {
		out[k] = v
	}
	return out, nil
}

func quietCronLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func twoServiceCobaltfile() *cobaltfile.Cobaltfile {
	return &cobaltfile.Cobaltfile{
		Version: "1.0",
		Services: map[string]cobaltfile.Service{
			"web": {Type: cobaltfile.TypeContainer, Image: "default", Port: 3000},
			"backup-database": {
				Type:     cobaltfile.TypeCron,
				Image:    "default",
				Schedule: "0 2 * * *",
				Command:  "/scripts/backup.sh",
			},
			"cleanup-tokens": {
				Type:     cobaltfile.TypeCron,
				Image:    "default",
				Schedule: "*/15 * * * *",
				Command:  "/scripts/cleanup.sh",
			},
		},
	}
}

func TestCronManager_ReconcileAddsNewCrons(t *testing.T) {
	t.Parallel()
	sched := NewScheduler(quietCronLog())
	mgr := NewCronManager(sched, &fakeCronDocker{}, &fakeEnvProvider{}, quietCronLog())

	project := store.Project{ID: 1, Name: "api"}
	dep := store.Deployment{ID: 11, ProjectID: 1, Number: 4}

	if err := mgr.Reconcile(context.Background(), project, dep, twoServiceCobaltfile()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	views := mgr.ListForProject("api")
	if len(views) != 2 {
		t.Fatalf("got %d views, want 2", len(views))
	}
	if views[0].ServiceName != "backup-database" || views[1].ServiceName != "cleanup-tokens" {
		t.Errorf("unsorted: %+v", views)
	}
	if views[0].DeploymentNumber != 4 {
		t.Errorf("deployment number not threaded through: %+v", views[0])
	}
}

func TestCronManager_ReconcileReplacesChangedCron(t *testing.T) {
	t.Parallel()
	sched := NewScheduler(quietCronLog())
	mgr := NewCronManager(sched, &fakeCronDocker{}, &fakeEnvProvider{}, quietCronLog())

	project := store.Project{ID: 1, Name: "api"}
	dep := store.Deployment{ID: 11, ProjectID: 1, Number: 4}
	if err := mgr.Reconcile(context.Background(), project, dep, twoServiceCobaltfile()); err != nil {
		t.Fatal(err)
	}

	// Same name, different schedule + new deployment number.
	cf := twoServiceCobaltfile()
	cf.Services["backup-database"] = cobaltfile.Service{
		Type:     cobaltfile.TypeCron,
		Image:    "default",
		Schedule: "0 4 * * *", // changed
		Command:  "/scripts/backup.sh",
	}
	dep2 := store.Deployment{ID: 12, ProjectID: 1, Number: 5}
	if err := mgr.Reconcile(context.Background(), project, dep2, cf); err != nil {
		t.Fatalf("re-Reconcile: %v", err)
	}

	views := mgr.ListForProject("api")
	if len(views) != 2 {
		t.Fatalf("len: %d", len(views))
	}
	for _, v := range views {
		if v.ServiceName == "backup-database" {
			if v.Schedule != "0 4 * * *" {
				t.Errorf("backup-database schedule didn't update: %+v", v)
			}
			if v.DeploymentNumber != 5 {
				t.Errorf("backup-database deployment didn't bump: %+v", v)
			}
		}
	}
}

func TestCronManager_ReconcileRemovesGoneCrons(t *testing.T) {
	t.Parallel()
	sched := NewScheduler(quietCronLog())
	mgr := NewCronManager(sched, &fakeCronDocker{}, &fakeEnvProvider{}, quietCronLog())

	project := store.Project{ID: 1, Name: "api"}
	dep := store.Deployment{ID: 11, ProjectID: 1, Number: 4}
	if err := mgr.Reconcile(context.Background(), project, dep, twoServiceCobaltfile()); err != nil {
		t.Fatal(err)
	}

	// New cobaltfile drops backup-database entirely.
	cf := &cobaltfile.Cobaltfile{
		Version: "1.0",
		Services: map[string]cobaltfile.Service{
			"web": {Type: cobaltfile.TypeContainer, Image: "default", Port: 3000},
			"cleanup-tokens": {
				Type:     cobaltfile.TypeCron,
				Image:    "default",
				Schedule: "*/15 * * * *",
				Command:  "/scripts/cleanup.sh",
			},
		},
	}
	dep2 := store.Deployment{ID: 12, ProjectID: 1, Number: 5}
	if err := mgr.Reconcile(context.Background(), project, dep2, cf); err != nil {
		t.Fatalf("re-Reconcile: %v", err)
	}

	views := mgr.ListForProject("api")
	if len(views) != 1 {
		t.Fatalf("len: %d, want 1", len(views))
	}
	if views[0].ServiceName != "cleanup-tokens" {
		t.Errorf("wrong cron retained: %+v", views[0])
	}
}

func TestCronManager_RemoveAllForProjectScopedToProject(t *testing.T) {
	t.Parallel()
	sched := NewScheduler(quietCronLog())
	mgr := NewCronManager(sched, &fakeCronDocker{}, &fakeEnvProvider{}, quietCronLog())

	pa := store.Project{ID: 1, Name: "api"}
	pb := store.Project{ID: 2, Name: "billing"}
	dep := store.Deployment{ID: 11, Number: 1}
	if err := mgr.Reconcile(context.Background(), pa, dep, twoServiceCobaltfile()); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Reconcile(context.Background(), pb, dep, twoServiceCobaltfile()); err != nil {
		t.Fatal(err)
	}

	if err := mgr.RemoveAllForProject("api"); err != nil {
		t.Fatal(err)
	}
	if v := mgr.ListForProject("api"); len(v) != 0 {
		t.Errorf("api crons not removed: %d remaining", len(v))
	}
	if v := mgr.ListForProject("billing"); len(v) != 2 {
		t.Errorf("billing crons collateral-damaged: %d remaining (want 2)", len(v))
	}
}

func TestCronManager_FireRunsContainerWithExpectedShape(t *testing.T) {
	t.Parallel()
	fdocker := &fakeCronDocker{}
	fenv := &fakeEnvProvider{
		envByProject: map[int64]map[string]string{
			1: {"USER_VAR": "user-value"},
		},
	}
	sched := NewScheduler(quietCronLog())
	mgr := NewCronManager(sched, fdocker, fenv, quietCronLog())

	project := store.Project{ID: 1, Name: "api"}
	dep := store.Deployment{ID: 11, ProjectID: 1, Number: 4}
	cf := &cobaltfile.Cobaltfile{
		Version: "1.0",
		Services: map[string]cobaltfile.Service{
			"backup-database": {
				Type:     cobaltfile.TypeCron,
				Image:    "default",
				Schedule: "0 2 * * *",
				Command:  "/scripts/backup.sh",
			},
		},
	}
	if err := mgr.Reconcile(context.Background(), project, dep, cf); err != nil {
		t.Fatal(err)
	}

	// Pull the cron's job out and call it directly — we don't want to
	// wait for the scheduler to fire on its own.
	entry := mgr.byName[SchedulerName("api", "backup-database")]
	job := mgr.cronJob(project, entry)
	job(context.Background())

	if len(fdocker.runs) != 1 {
		t.Fatalf("docker.Run called %d times, want 1", len(fdocker.runs))
	}
	got := fdocker.runs[0]
	if got.Image != "cobalt/project-api-default:4" {
		t.Errorf("image: got %q", got.Image)
	}
	if got.ProjectID != 1 || got.ProjectName != "api" {
		t.Errorf("project labels: %+v", got)
	}
	if got.ServiceName != "backup-database" {
		t.Errorf("service label: %+v", got)
	}
	if got.DeploymentNumber != 4 {
		t.Errorf("deployment label: %+v", got)
	}
	if len(got.Networks) != 2 ||
		got.Networks[0] != "cobalt-project-api-4" ||
		got.Networks[1] != "cobalt-main" {
		t.Errorf("networks: %+v", got.Networks)
	}
	if got.EnvVars["USER_VAR"] != "user-value" {
		t.Error("user env var not threaded through")
	}
	for _, k := range []string{"COBALT_PROJECT_NAME", "COBALT_SERVICE_NAME", "COBALT_DEPLOYMENT_NUMBER"} {
		if _, ok := got.EnvVars[k]; !ok {
			t.Errorf("missing daemon-injected env var %q", k)
		}
	}
	if len(got.Command) != 3 || got.Command[0] != "sh" || got.Command[1] != "-c" || got.Command[2] != "/scripts/backup.sh" {
		t.Errorf("command: %+v", got.Command)
	}
}

func TestCronManager_FireFailureLogsButDoesNotPanic(t *testing.T) {
	t.Parallel()
	fdocker := &fakeCronDocker{runErr: errors.New("docker said no")}
	sched := NewScheduler(quietCronLog())
	mgr := NewCronManager(sched, fdocker, &fakeEnvProvider{}, quietCronLog())

	project := store.Project{ID: 1, Name: "api"}
	dep := store.Deployment{ID: 11, Number: 4}
	cf := &cobaltfile.Cobaltfile{
		Version: "1.0",
		Services: map[string]cobaltfile.Service{
			"failing-cron": {
				Type:     cobaltfile.TypeCron,
				Image:    "default",
				Schedule: "*/5 * * * *",
				Command:  "exit 1",
			},
		},
	}
	if err := mgr.Reconcile(context.Background(), project, dep, cf); err != nil {
		t.Fatal(err)
	}
	entry := mgr.byName[SchedulerName("api", "failing-cron")]
	job := mgr.cronJob(project, entry)
	// Shouldn't panic; just logs the failure.
	job(context.Background())
	if len(fdocker.runs) != 1 {
		t.Errorf("expected one Run attempt, got %d", len(fdocker.runs))
	}
}

func TestCronManager_NilCobaltfileTreatedAsRemoveAll(t *testing.T) {
	t.Parallel()
	sched := NewScheduler(quietCronLog())
	mgr := NewCronManager(sched, &fakeCronDocker{}, &fakeEnvProvider{}, quietCronLog())

	project := store.Project{ID: 1, Name: "api"}
	dep := store.Deployment{ID: 11, Number: 4}
	if err := mgr.Reconcile(context.Background(), project, dep, twoServiceCobaltfile()); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Reconcile(context.Background(), project, dep, nil); err != nil {
		t.Fatal(err)
	}
	if v := mgr.ListForProject("api"); len(v) != 0 {
		t.Errorf("expected empty after nil cobaltfile, got %d entries", len(v))
	}
}
