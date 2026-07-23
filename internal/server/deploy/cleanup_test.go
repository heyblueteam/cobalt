package deploy

import (
	"context"
	"io"
	"sort"
	"testing"

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

func TestCleanupOldServices_KeepsCurrentAndGraceWeb(t *testing.T) {
	t.Parallel()
	d := &fakeCleanupDocker{services: svcs(
		"api-7-web", "api-7-worker", // current deployment — keep
		"api-6-web",    // most recent prior web — keep for grace
		"api-6-worker", // prior worker — reap (no public route)
		"api-5-web",    // older web — reap
	)}
	cleanupOldServices(context.Background(), quietLog(), d,
		store.Project{ID: 1, Name: "api"}, store.Deployment{Number: 7}, false, io.Discard)

	got := append([]string(nil), d.removed...)
	sort.Strings(got)
	want := []string{"api-5-web", "api-6-worker"}
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
		store.Project{ID: 1, Name: "api"}, store.Deployment{Number: 1}, false, io.Discard)
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
		store.Project{ID: 1, Name: "my-app"}, store.Deployment{Number: 7}, false, io.Discard)
	// Keep current (7) + grace (6); reap only 5.
	if len(d.removed) != 1 || d.removed[0] != "my-app-5-web" {
		t.Errorf("removed %v, want [my-app-5-web]", d.removed)
	}
}

func TestCleanupOldServices_KeepsStablePublicWebOnlyWhileEnabled(t *testing.T) {
	t.Parallel()
	d := &fakeCleanupDocker{services: svcs("cobalt-web-1", "api-7-web")}
	project := store.Project{ID: 1, Name: "api"}
	cleanupOldServices(context.Background(), quietLog(), d, project, store.Deployment{Number: 7}, true, io.Discard)
	if len(d.removed) != 0 {
		t.Errorf("stable mode removed %v", d.removed)
	}

	cleanupOldServices(context.Background(), quietLog(), d, project, store.Deployment{Number: 7}, false, io.Discard)
	if len(d.removed) != 1 || d.removed[0] != "cobalt-web-1" {
		t.Errorf("direct-mode revert removed %v, want [cobalt-web-1]", d.removed)
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
