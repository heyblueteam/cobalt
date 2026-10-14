package deploy

import (
	"context"
	"io"
	"sort"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

type fakeCleanupDocker struct {
	services []docker.ServiceInfo
	removed  []string
	listErr  error
}

func (f *fakeCleanupDocker) ListServicesForProject(context.Context, int64) ([]docker.ServiceInfo, error) {
	return f.services, f.listErr
}

func (f *fakeCleanupDocker) RemoveService(_ context.Context, name string) error {
	f.removed = append(f.removed, name)
	return nil
}

func svcs(names ...string) []docker.ServiceInfo {
	out := make([]docker.ServiceInfo, len(names))
	for i, n := range names {
		out[i] = docker.ServiceInfo{Name: n}
	}
	return out
}

func TestCleanupOldServices_KeepsEveryWebEndpointUntilGuardedReap(t *testing.T) {
	t.Parallel()
	d := &fakeCleanupDocker{services: svcs(
		"api-7-web", "api-7-worker", // current deployment — keep
		"api-6-web",    // most recent prior web — keep for grace
		"api-6-worker", // prior worker — reap (no public route)
		"api-5-web",    // older web — keep until guarded reaper
	)}
	cleanupOldServices(context.Background(), quietLog(), d,
		store.Project{ID: 1, Name: "api"}, store.Deployment{Number: 7}, true, io.Discard)

	got := append([]string(nil), d.removed...)
	sort.Strings(got)
	want := []string{"api-6-worker"}
	if len(got) != len(want) {
		t.Fatalf("removed %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("removed %v, want %v", got, want)
		}
	}
}

func TestCleanupOldServices_FirstDeploy_NothingToReap(t *testing.T) {
	t.Parallel()
	d := &fakeCleanupDocker{services: svcs("api-1-web", "api-1-worker")}
	cleanupOldServices(context.Background(), quietLog(), d,
		store.Project{ID: 1, Name: "api"}, store.Deployment{Number: 1}, true, io.Discard)
	if len(d.removed) != 0 {
		t.Errorf("first deploy should reap nothing, removed %v", d.removed)
	}
}

func TestCleanupOldServices_HyphenatedProjectName(t *testing.T) {
	t.Parallel()
	d := &fakeCleanupDocker{services: svcs(
		"my-app-7-web", "my-app-6-web", "my-app-5-web",
	)}
	cleanupOldServices(context.Background(), quietLog(), d,
		store.Project{ID: 1, Name: "my-app"}, store.Deployment{Number: 7}, true, io.Discard)
	// Fast subsequent deploys must not bypass the web retirement grace floor.
	if len(d.removed) != 0 {
		t.Errorf("removed web endpoints before guarded reap: %v", d.removed)
	}
}

func TestCleanupOldServices_KeepsStablePublicWebDuringRevert(t *testing.T) {
	t.Parallel()
	d := &fakeCleanupDocker{services: svcs("cobalt-web-1", "api-7-web")}
	project := store.Project{ID: 1, Name: "api"}
	cleanupOldServices(context.Background(), quietLog(), d, project, store.Deployment{Number: 7}, true, io.Discard)
	if len(d.removed) != 0 {
		t.Errorf("stable mode removed %v", d.removed)
	}

	cleanupOldServices(context.Background(), quietLog(), d, project, store.Deployment{Number: 7}, true, io.Discard)
	if len(d.removed) != 0 {
		t.Errorf("direct-mode revert prematurely removed %v", d.removed)
	}
}

func TestCleanupOldServices_NonPublicDestinationKeepsOneGeneration(t *testing.T) {
	t.Parallel()
	d := &fakeCleanupDocker{services: svcs("cobalt-web-1", "api-5-web", "api-6-web")}
	cleanupOldServices(context.Background(), quietLog(), d, store.Project{ID: 1, Name: "api"}, store.Deployment{Number: 7}, false, io.Discard)
	if len(d.removed) != 2 {
		t.Fatalf("non-public cleanup changed its existing one-generation grace policy: %v", d.removed)
	}
}

func TestCleanupOldServices_InternalWebDoesNotDeferEveryGeneration(t *testing.T) {
	t.Parallel()
	cf := &cobaltfile.Cobaltfile{Services: map[string]cobaltfile.Service{"web": {Type: cobaltfile.TypeContainer, ExposedInternally: true}}}
	d := &fakeCleanupDocker{services: svcs("internal-7-web", "internal-6-web", "internal-5-web")}
	cleanupOldServices(context.Background(), quietLog(), d, store.Project{ID: 9, Name: "internal"}, store.Deployment{Number: 7}, deferPublicWebCleanup(cf), io.Discard)
	if len(d.removed) != 1 || d.removed[0] != "internal-5-web" {
		t.Fatalf("internal service cleanup leaked generations: %v", d.removed)
	}
}

func TestGraceWebService(t *testing.T) {
	t.Parallel()
	services := svcs("api-7-web", "api-6-web", "api-6-worker", "api-5-web", "other-9-web")
	got := graceWebService(services, "api", 7)
	if got != "api-6-web" {
		t.Errorf("graceWebService = %q, want api-6-web", got)
	}
	// No prior web → "".
	if got := graceWebService(svcs("api-1-web"), "api", 1); got != "" {
		t.Errorf("graceWebService (first deploy) = %q, want \"\"", got)
	}
	// Only a prior worker (no web) → "" (nothing to fall back to).
	if got := graceWebService(svcs("api-6-worker"), "api", 7); got != "" {
		t.Errorf("graceWebService (worker only) = %q, want \"\"", got)
	}
}
