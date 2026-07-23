package deploy

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

type serviceDockerFake struct {
	created        []docker.ServiceCreateOpts
	stableCreated  []docker.ServiceCreateOpts
	healthChecks   []healthCheckCall
	removed        []string
	services       []docker.ServiceInfo
	createErr      error
	healthCheckErr error
	removeErr      error
	listErr        error
}

type healthCheckCall struct {
	name     string
	replicas int
	timeout  time.Duration
}

func (f *serviceDockerFake) CreateService(_ context.Context, opts docker.ServiceCreateOpts) error {
	f.created = append(f.created, opts)
	return f.createErr
}

func (f *serviceDockerFake) ReconcileStableService(_ context.Context, opts docker.ServiceCreateOpts) error {
	f.stableCreated = append(f.stableCreated, opts)
	return f.createErr
}

func TestStartServicesPhase_UsesStablePublicWeb(t *testing.T) {
	t.Parallel()
	d := &serviceDockerFake{}
	project := store.Project{ID: 7, Name: "api"}
	dep := store.Deployment{Number: 3}
	built := []BuiltService{{Name: "web", Service: cobaltfile.Service{Type: cobaltfile.TypeContainer}, ImageTag: "api:3"}}
	started, err := startServicesPhase(context.Background(), d, project, dep, built, nil, "cobalt-project-api-3", true, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(started) != 0 || len(d.created) != 0 || len(d.stableCreated) != 1 {
		t.Fatalf("started=%v created=%v stable=%v", started, d.created, d.stableCreated)
	}
	got := d.stableCreated[0]
	if got.Name != "cobalt-web-7" || len(got.Networks) != 1 || got.Networks[0].Name != MainNetworkName {
		t.Errorf("stable opts = %+v", got)
	}
}

func (f *serviceDockerFake) WaitForServiceHealthy(_ context.Context, name string, replicas int, timeout time.Duration) error {
	f.healthChecks = append(f.healthChecks, healthCheckCall{name: name, replicas: replicas, timeout: timeout})
	return f.healthCheckErr
}

func (f *serviceDockerFake) RemoveService(_ context.Context, name string) error {
	f.removed = append(f.removed, name)
	return f.removeErr
}

func (f *serviceDockerFake) ListServicesForDeployment(_ context.Context, _ int64, _ int) ([]docker.ServiceInfo, error) {
	return f.services, f.listErr
}

func (f *serviceDockerFake) ListServicesForProject(_ context.Context, _ int64) ([]docker.ServiceInfo, error) {
	return f.services, f.listErr
}

// findAttachment returns the NetworkAttachment matching name, or fails the
// test if absent. Centralizes the "ordering is implementation detail, but
// each network must be present exactly once" assertion the alias tests all
// share.
func findAttachment(t *testing.T, atts []docker.NetworkAttachment, name string) docker.NetworkAttachment {
	t.Helper()
	var hits []docker.NetworkAttachment
	for _, a := range atts {
		if a.Name == name {
			hits = append(hits, a)
		}
	}
	if len(hits) == 0 {
		t.Fatalf("no NetworkAttachment for %q in %+v", name, atts)
	}
	if len(hits) > 1 {
		t.Fatalf("multiple NetworkAttachments for %q in %+v — every service must attach to each network exactly once", name, atts)
	}
	return hits[0]
}

// TestServiceCreateOpts_AliasesExposedInternally is the core happy-path
// case: a service with `exposedInternally: true` must answer to its short
// stable cross-project name on cobalt-main (e.g. `redis-redis`). This is
// what api's `REDIS_HOST=redis-redis` env var depends on at runtime.
// Without this alias, api's redis connection fails with NXDOMAIN.
func TestServiceCreateOpts_AliasesExposedInternally(t *testing.T) {
	t.Parallel()
	project := store.Project{ID: 7, Name: "redis"}
	dep := store.Deployment{Number: 1}
	b := BuiltService{
		Name: "redis",
		Service: cobaltfile.Service{
			ExposedInternally: true,
		},
		ImageTag: "redis/redis-stack:7.4.0-v8",
	}

	opts := serviceCreateOpts(project, dep, b, nil, "cobalt-project-redis-1")

	main := findAttachment(t, opts.Networks, MainNetworkName)
	if main.Alias != "redis-redis" {
		t.Errorf("cobalt-main alias = %q, want %q (stable cross-project name)", main.Alias, "redis-redis")
	}
	perDeploy := findAttachment(t, opts.Networks, "cobalt-project-redis-1")
	if perDeploy.Alias != "redis" {
		t.Errorf("per-deploy alias = %q, want %q (within-project short name)", perDeploy.Alias, "redis")
	}
}

// TestServiceCreateOpts_AliasesNotExposedInternally verifies the
// "fallback" branch: when exposedInternally is false, the cobalt-main
// alias is the full deployment-numbered service name. This preserves the
// invariant that every service has a non-empty alias on every attached
// network — useful for future inspection tooling and reconcilers — while
// avoiding accidental cross-project exposure of services the operator
// didn't opt-in.
func TestServiceCreateOpts_AliasesNotExposedInternally(t *testing.T) {
	t.Parallel()
	project := store.Project{ID: 7, Name: "api"}
	dep := store.Deployment{Number: 200}
	b := BuiltService{
		Name: "web",
		Service: cobaltfile.Service{
			ExposedInternally: false,
		},
		ImageTag: "cobalt/project-api-web:200",
	}

	opts := serviceCreateOpts(project, dep, b, nil, "cobalt-project-api-200")

	main := findAttachment(t, opts.Networks, MainNetworkName)
	if main.Alias != "api-200-web" {
		t.Errorf("cobalt-main alias = %q, want %q (fallback = full service name)", main.Alias, "api-200-web")
	}
	perDeploy := findAttachment(t, opts.Networks, "cobalt-project-api-200")
	if perDeploy.Alias != "web" {
		t.Errorf("per-deploy alias = %q, want %q", perDeploy.Alias, "web")
	}
}

// TestServiceCreateOpts_HyphenatedProjectName checks that hyphens in
// project / service names pass through untouched. The white-label* family
// of projects exercises this — if we ever do alias-name sanitization
// (uppercasing, hyphen->underscore, etc.), env vars like
// `FILES_HOST=white-label-files-web` would silently break.
func TestServiceCreateOpts_HyphenatedProjectName(t *testing.T) {
	t.Parallel()
	project := store.Project{ID: 12, Name: "white-label-files"}
	dep := store.Deployment{Number: 7}

	// exposedInternally branch — the alias should be project + "-" + service.
	bExposed := BuiltService{
		Name:    "web",
		Service: cobaltfile.Service{ExposedInternally: true},
	}
	opts := serviceCreateOpts(project, dep, bExposed, nil, "cobalt-project-white-label-files-7")
	if got := findAttachment(t, opts.Networks, MainNetworkName).Alias; got != "white-label-files-web" {
		t.Errorf("exposed alias = %q, want %q", got, "white-label-files-web")
	}

	// non-exposed branch — falls back to full service name.
	bUnexposed := BuiltService{Name: "web"}
	opts2 := serviceCreateOpts(project, dep, bUnexposed, nil, "cobalt-project-white-label-files-7")
	if got := findAttachment(t, opts2.Networks, MainNetworkName).Alias; got != "white-label-files-7-web" {
		t.Errorf("unexposed alias = %q, want %q", got, "white-label-files-7-web")
	}
}

// TestServiceCreateOpts_AttachesBothNetworks documents the
// always-two-networks contract: every cobalt-managed service joins both
// the per-deployment overlay AND cobalt-main. The deploy orchestrator,
// caddy reconciler, hooks, and one-shot containers all assume this; a
// regression that drops cobalt-main would silently break cross-project
// communication.
func TestServiceCreateOpts_AttachesBothNetworks(t *testing.T) {
	t.Parallel()
	project := store.Project{ID: 1, Name: "api"}
	dep := store.Deployment{Number: 1}
	b := BuiltService{Name: "web"}

	opts := serviceCreateOpts(project, dep, b, nil, "cobalt-project-api-1")

	if len(opts.Networks) != 2 {
		t.Fatalf("got %d networks, want 2: %+v", len(opts.Networks), opts.Networks)
	}
	// The per-deployment network must be first; some downstream tooling
	// treats the first network as primary.
	if opts.Networks[0].Name != "cobalt-project-api-1" {
		t.Errorf("Networks[0] = %q, want per-deployment network first", opts.Networks[0].Name)
	}
	if opts.Networks[1].Name != MainNetworkName {
		t.Errorf("Networks[1] = %q, want cobalt-main second", opts.Networks[1].Name)
	}
}

func TestServiceCreateOpts_UsesMinReplicas(t *testing.T) {
	t.Parallel()
	project := store.Project{ID: 1, Name: "api"}
	dep := store.Deployment{Number: 1}
	b := BuiltService{
		Name: "web",
		Service: cobaltfile.Service{
			MinReplicas: 4,
		},
	}

	opts := serviceCreateOpts(project, dep, b, nil, "cobalt-project-api-1")

	if opts.Replicas != 4 {
		t.Errorf("Replicas = %d, want 4", opts.Replicas)
	}
}

func TestServiceCreateOpts_DefaultsReplicasToOne(t *testing.T) {
	t.Parallel()
	project := store.Project{ID: 1, Name: "api"}
	dep := store.Deployment{Number: 1}
	b := BuiltService{Name: "web"}

	opts := serviceCreateOpts(project, dep, b, nil, "cobalt-project-api-1")

	if opts.Replicas != 1 {
		t.Errorf("Replicas = %d, want 1", opts.Replicas)
	}
}

func TestWaitHealthyAll_UsesMinReplicas(t *testing.T) {
	t.Parallel()
	project := store.Project{ID: 1, Name: "api"}
	dep := store.Deployment{Number: 3}
	d := &serviceDockerFake{}
	built := []BuiltService{
		{
			Name: "web",
			Service: cobaltfile.Service{
				Type:        cobaltfile.TypeContainer,
				MinReplicas: 4,
			},
		},
	}

	if err := waitHealthyAll(context.Background(), d, project, dep, built, false, io.Discard); err != nil {
		t.Fatalf("waitHealthyAll: %v", err)
	}
	if len(d.healthChecks) != 1 {
		t.Fatalf("health checks = %d, want 1", len(d.healthChecks))
	}
	got := d.healthChecks[0]
	if got.name != "api-3-web" {
		t.Errorf("health check name = %q, want api-3-web", got.name)
	}
	if got.replicas != 4 {
		t.Errorf("health check replicas = %d, want 4", got.replicas)
	}
	if got.timeout != HealthcheckTimeout {
		t.Errorf("health check timeout = %s, want %s", got.timeout, HealthcheckTimeout)
	}
}

// TestMainNetAlias_PureFunction exercises the alias-picker directly to
// pin down the contract: exposedInternally selects the short stable form,
// otherwise the full deployment-numbered name. Useful so a regression in
// either branch shows up at the unit level even if higher-level tests
// happen to mask it.
func TestMainNetAlias_PureFunction(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name              string
		project           string
		service           string
		deployment        int
		exposedInternally bool
		want              string
	}{
		{"exposed_simple", "redis", "redis", 1, true, "redis-redis"},
		{"exposed_different_service_name", "plunk", "web", 6, true, "plunk-web"},
		{"unexposed_full_name", "api", "web", 200, false, "api-200-web"},
		{"unexposed_at_deployment_1", "api", "web", 1, false, "api-1-web"},
		{"hyphenated_project_exposed", "white-label-files", "web", 7, true, "white-label-files-web"},
		{"hyphenated_project_unexposed", "white-label-files", "web", 7, false, "white-label-files-7-web"},
		{"multi_service_project", "openpanel", "clickhouse", 2, true, "openpanel-clickhouse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			project := store.Project{Name: tc.project}
			dep := store.Deployment{Number: tc.deployment}
			b := BuiltService{
				Name: tc.service,
				Service: cobaltfile.Service{
					ExposedInternally: tc.exposedInternally,
				},
			}
			if got := mainNetAlias(project, b, dep); got != tc.want {
				t.Errorf("mainNetAlias(%s, %s, deploy=%d, exposed=%v) = %q, want %q",
					tc.project, tc.service, tc.deployment, tc.exposedInternally, got, tc.want)
			}
		})
	}
}
