package deploy

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func TestCommitDisplay(t *testing.T) {
	t.Parallel()
	long := "abcdef1234567890"
	short := "abc"
	empty := ""
	cases := []struct {
		name string
		in   *string
		want string
	}{
		{"nil", nil, "(unknown)"},
		{"empty", &empty, "(unknown)"},
		{"short", &short, "abc"},
		{"long_truncated_to_7", &long, "abcdef1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := commitDisplay(c.in); got != c.want {
				t.Errorf("commitDisplay: got %q, want %q", got, c.want)
			}
		})
	}
}

func TestCountImageBuilds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   []BuiltService
		want int
	}{
		{"empty", nil, 0},
		{"all_empty_tags", []BuiltService{{ImageTag: ""}, {ImageTag: ""}}, 0},
		{"one_image", []BuiltService{{ImageTag: "a:1"}, {ImageTag: ""}}, 1},
		{"dedupes_repeats", []BuiltService{
			{ImageTag: "a:1"}, {ImageTag: "a:1"}, {ImageTag: "b:1"},
		}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := countImageBuilds(c.in); got != c.want {
				t.Errorf("countImageBuilds: got %d, want %d", got, c.want)
			}
		})
	}
}

// reconstructBuiltServicesFromTarget ------------------------------------

func TestReconstructBuilt_NoServicesErrors(t *testing.T) {
	t.Parallel()
	o, _, _, _, _ := setupOrchestrator(t)
	cf := &cobaltfile.Cobaltfile{Services: map[string]cobaltfile.Service{}}
	_, err := reconstructBuiltServicesFromTarget(context.Background(), o, "api", 1, cf)
	if err == nil || !strings.Contains(err.Error(), "no services") {
		t.Errorf("expected no-services error, got %v", err)
	}
}

func TestReconstructBuilt_ImageMissingReturnsSentinel(t *testing.T) {
	t.Parallel()
	o, fdocker, _, _, _ := setupOrchestrator(t)
	// fakeDockerRunner default: empty stdout for image ls → not cached.
	fdocker.stdout["image ls"] = ""

	cf := &cobaltfile.Cobaltfile{
		Services: map[string]cobaltfile.Service{
			"web": {Type: cobaltfile.TypeContainer, Image: "default", Port: 3000},
		},
	}
	_, err := reconstructBuiltServicesFromTarget(context.Background(), o, "api", 5, cf)
	if !errors.Is(err, ErrRollbackImageMissing) {
		t.Fatalf("expected ErrRollbackImageMissing, got %v", err)
	}
	if !strings.Contains(err.Error(), "cobalt deploy") {
		t.Errorf("error should hint redeploy command, got %v", err)
	}
}

func TestReconstructBuilt_ImageProbeFailurePropagates(t *testing.T) {
	t.Parallel()
	o, fdocker, _, _, _ := setupOrchestrator(t)
	fdocker.errs["image ls"] = errors.New("docker daemon down")

	cf := &cobaltfile.Cobaltfile{
		Services: map[string]cobaltfile.Service{
			"web": {Type: cobaltfile.TypeContainer, Image: "default", Port: 3000},
		},
	}
	_, err := reconstructBuiltServicesFromTarget(context.Background(), o, "api", 5, cf)
	if err == nil || !strings.Contains(err.Error(), "docker daemon down") {
		t.Fatalf("expected probe error, got %v", err)
	}
	if errors.Is(err, ErrRollbackImageMissing) {
		t.Error("probe failure should not be reported as ErrRollbackImageMissing")
	}
}

func TestReconstructBuilt_ProbesEachImageOnce(t *testing.T) {
	t.Parallel()
	o, fdocker, _, _, _ := setupOrchestrator(t)
	// "image ls ... <tag>" returns the tag line ⇒ exists.
	tag1 := docker.InternalImageName("api", "default", 3)
	tag2 := docker.InternalImageName("api", "worker", 3)
	fdocker.stdout["image ls"] = tag1 + "\n" + tag2 + "\n"

	cf := &cobaltfile.Cobaltfile{
		Services: map[string]cobaltfile.Service{
			// Two services share image "default" — probe must dedupe.
			"web":     {Type: cobaltfile.TypeContainer, Image: "default", Port: 3000},
			"api":     {Type: cobaltfile.TypeContainer, Image: "default", Port: 3000},
			"worker":  {Type: cobaltfile.TypeContainer, Image: "worker"},
			"nightly": {Type: cobaltfile.TypeCron, Schedule: "0 0 * * *", Command: "x"},
		},
	}
	built, err := reconstructBuiltServicesFromTarget(context.Background(), o, "api", 3, cf)
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	if len(built) != 4 {
		t.Fatalf("built len: got %d, want 4", len(built))
	}
	// Cron service must end up with empty ImageTag (no image probe).
	for _, b := range built {
		if b.Name == "nightly" && b.ImageTag != "" {
			t.Errorf("cron service unexpectedly has image tag %q", b.ImageTag)
		}
		if b.Name == "web" && b.ImageTag != tag1 {
			t.Errorf("web tag: got %q, want %q", b.ImageTag, tag1)
		}
	}
	// Dedupe check: 3 distinct images expected, but only 2 actually need probing
	// (default, worker). Count "image ls" invocations.
	var probes int
	fdocker.mu.Lock()
	for _, call := range fdocker.calls {
		if strings.HasPrefix(call, "image ls") {
			probes++
		}
	}
	fdocker.mu.Unlock()
	if probes != 2 {
		t.Errorf("image probes: got %d, want 2 (dedupe broken)", probes)
	}
}

func TestReconstructBuilt_EmptyImageFallsBackToDefault(t *testing.T) {
	t.Parallel()
	o, fdocker, _, _, _ := setupOrchestrator(t)
	tag := docker.InternalImageName("api", "default", 2)
	fdocker.stdout["image ls"] = tag + "\n"

	cf := &cobaltfile.Cobaltfile{
		Services: map[string]cobaltfile.Service{
			// Image unset → must be treated as "default".
			"web": {Type: cobaltfile.TypeContainer, Port: 3000},
		},
	}
	built, err := reconstructBuiltServicesFromTarget(context.Background(), o, "api", 2, cf)
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	if len(built) != 1 || built[0].ImageTag != tag {
		t.Errorf("got %+v, want one service with tag %q", built, tag)
	}
}

// rollbackRun ----------------------------------------------------------

func TestRollbackRun_RequiresRollbackOf(t *testing.T) {
	t.Parallel()
	o, _, _, db, project := setupOrchestrator(t)
	// Deployment with RollbackOf=nil — illegal for rollbackRun.
	dep := store.Deployment{ID: 999, ProjectID: project.ID, Number: 2}
	err := o.rollbackRun(context.Background(), quietLog(), &project, dep, nil, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "RollbackOf must be set") {
		t.Errorf("expected RollbackOf-required error, got %v", err)
	}
	_ = db
}

func TestRollbackRun_TargetMissingCobaltfileErrors(t *testing.T) {
	t.Parallel()
	o, _, _, db, project := setupOrchestrator(t)

	// Create a "target" deployment WITHOUT a resolved cobaltfile.
	targetID, err := db.CreateDeployment(context.Background(), store.Deployment{
		ProjectID: project.ID, Number: 1, Status: cobaltapi.StateSuccess,
	})
	if err != nil {
		t.Fatal(err)
	}

	dep := store.Deployment{
		ID: 999, ProjectID: project.ID, Number: 2, RollbackOf: &targetID,
	}
	err = o.rollbackRun(context.Background(), quietLog(), &project, dep, nil, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no recorded cobaltfile") {
		t.Errorf("expected missing-cobaltfile error, got %v", err)
	}
}

// TestRollbackRun_HappyPath exercises the rollback path end-to-end via
// Orchestrator.Run with a RollbackOf-set deployment. The orchestrator
// must skip prepare + build (which is enforced by leaving fakePrep/
// fakeBuild defaulted — if they ran they'd fire) and proceed straight to
// cutover, swapping Caddy to the target deployment's services.
func TestRollbackRun_HappyPath(t *testing.T) {
	t.Parallel()
	o, fdocker, fcaddy, db, project := setupOrchestrator(t)
	ctx := context.Background()

	// Seed: target deployment #1 succeeded earlier and recorded its
	// cobaltfile.  The cached image tag the rollback will probe is
	// cobalt/project-api-default:1.
	targetID, _ := db.CreateDeployment(ctx, store.Deployment{
		ProjectID: project.ID, Number: 1, Status: cobaltapi.StateSuccess,
	})
	resolvedCF := `{
		"version": "1.0",
		"services": {
			"web": {"type": "container", "image": "default", "port": 3000}
		},
		"images": {
			"default": {"dockerfile": "Dockerfile", "context": "."}
		}
	}`
	if err := db.SetResolvedCobaltfile(ctx, targetID, resolvedCF); err != nil {
		t.Fatal(err)
	}

	tag := docker.InternalImageName(project.Name, "default", 1)
	fdocker.stdout["image ls"] = tag + "\n"

	// If prepare/build fire, the test fails (their fakes return defaults
	// pointing at deployment 1, not the rollback row).
	o.Preparer = &fakePrep{err: errors.New("rollback should skip prepare")}
	o.Builder = &fakeBuild{err: errors.New("rollback should skip build")}

	// Enqueue the rollback row.
	q := NewQueue(db)
	id, _, err := q.EnqueueRollback(ctx, project.ID, targetID)
	if err != nil {
		t.Fatalf("EnqueueRollback: %v", err)
	}
	_ = db.SetDeploymentStatus(ctx, id, cobaltapi.StateFetching)
	dep, err := db.GetDeployment(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if err := o.Run(ctx, *dep); err != nil {
		t.Fatalf("Run rollback: %v", err)
	}

	// Caddy was swapped — the rollback starts NEW services under its own
	// deployment number (2) but reusing the target's cached image. The
	// container name reflects the new row, not the target.
	fcaddy.mu.Lock()
	got := fcaddy.upstreams[project.ID]
	fcaddy.mu.Unlock()
	wantUpstream := "api-2-web:3000"
	if got != wantUpstream {
		t.Errorf("Caddy upstream after rollback: got %q, want %q", got, wantUpstream)
	}
	// Image must have been probed (not rebuilt).
	if !fdocker.hasCall("image ls") {
		t.Error("rollback did not probe cached image")
	}
	// Build never ran ⇒ no `docker build` invocation.
	if fdocker.hasCall("build") {
		t.Error("rollback unexpectedly invoked docker build")
	}

	// Rollback row should now have the same resolved cobaltfile as target.
	refetched, _ := db.GetDeployment(ctx, dep.ID)
	if refetched.ResolvedCobaltfile == nil || *refetched.ResolvedCobaltfile != resolvedCF {
		t.Error("rollback row's resolved_cobaltfile not persisted from target")
	}
}

func TestRollbackRun_ImageMissingReturnsSentinel(t *testing.T) {
	t.Parallel()
	o, fdocker, _, db, project := setupOrchestrator(t)
	ctx := context.Background()

	targetID, _ := db.CreateDeployment(ctx, store.Deployment{
		ProjectID: project.ID, Number: 1, Status: cobaltapi.StateSuccess,
	})
	resolvedCF := `{
		"version": "1.0",
		"services": {
			"web": {"type": "container", "image": "default", "port": 3000}
		}
	}`
	_ = db.SetResolvedCobaltfile(ctx, targetID, resolvedCF)

	// Deliberately leave image ls empty ⇒ image probe says "missing".
	fdocker.stdout["image ls"] = ""

	q := NewQueue(db)
	id, _, _ := q.EnqueueRollback(ctx, project.ID, targetID)
	_ = db.SetDeploymentStatus(ctx, id, cobaltapi.StateFetching)
	dep, _ := db.GetDeployment(ctx, id)

	err := o.Run(ctx, *dep)
	if !errors.Is(err, ErrRollbackImageMissing) {
		t.Fatalf("expected ErrRollbackImageMissing, got %v", err)
	}
	// No service create attempt — refused at the preflight image probe.
	if fdocker.hasCall("service create") {
		t.Error("services started despite missing cached image")
	}
}

// TestRollbackRun_CronOnlyTargetSkipsImageProbe asserts that a cron-only
// rollback (no container services) doesn't fail at the image-cache check
// when no images exist — there is nothing to probe.
func TestRollbackRun_CronOnlyTargetSkipsImageProbe(t *testing.T) {
	t.Parallel()
	o, fdocker, fcaddy, db, project := setupOrchestrator(t)
	ctx := context.Background()

	targetID, _ := db.CreateDeployment(ctx, store.Deployment{
		ProjectID: project.ID, Number: 1, Status: cobaltapi.StateSuccess,
	})
	resolvedCF := `{
		"version": "1.0",
		"services": {
			"nightly": {
				"type": "cron",
				"schedule": "0 0 * * *",
				"command": "node nightly.js",
				"image": "default"
			}
		}
	}`
	_ = db.SetResolvedCobaltfile(ctx, targetID, resolvedCF)
	// Cron services still reference an image (the `image: default` line)
	// and cron is type=cron (not container), but reconstructBuilt only
	// skips the type≠container case. Adjust expectations: image probe
	// runs for type=cron is false; the cron's type filter exits early.
	// Provide an empty image-ls anyway and assert success.
	fdocker.stdout["image ls"] = ""

	q := NewQueue(db)
	id, _, _ := q.EnqueueRollback(ctx, project.ID, targetID)
	_ = db.SetDeploymentStatus(ctx, id, cobaltapi.StateFetching)
	dep, _ := db.GetDeployment(ctx, id)

	if err := o.Run(ctx, *dep); err != nil {
		t.Fatalf("cron-only rollback: %v", err)
	}
	// No web ⇒ Caddy left untouched.
	fcaddy.mu.Lock()
	defer fcaddy.mu.Unlock()
	if len(fcaddy.upstreams) != 0 {
		t.Errorf("Caddy touched on cron-only rollback: %v", fcaddy.upstreams)
	}
}
