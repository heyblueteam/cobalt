package worker

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

type fakeNetworkOps struct {
	mu        sync.Mutex
	byProject map[string][]docker.NetworkInfo
	listErr   error
	removeErr map[string]error // per-network error; nil → success
	removed   []string
}

func (f *fakeNetworkOps) ListNetworksForProject(_ context.Context, _ int64, projectName string) ([]docker.NetworkInfo, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.byProject[projectName], nil
}

func (f *fakeNetworkOps) RemoveNetwork(_ context.Context, name string) error {
	if err, ok := f.removeErr[name]; ok && err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, name)
	return nil
}

func TestCleanupNetworks_RemovesNonActive(t *testing.T) {
	t.Parallel()

	projects := &fakeProjectLister{
		projects: []store.Project{{ID: 1, Name: "api"}},
	}
	deploys := &fakeDeployLister{
		byProject: map[int64][]int{1: {7, 8}}, // 7 and 8 are still active
	}
	nets := &fakeNetworkOps{
		byProject: map[string][]docker.NetworkInfo{
			"api": {
				{Name: "cobalt-project-api-5", DeploymentNumber: 5},
				{Name: "cobalt-project-api-7", DeploymentNumber: 7},
				{Name: "cobalt-project-api-8", DeploymentNumber: 8},
				{Name: "cobalt-project-api-6", DeploymentNumber: 6},
			},
		},
	}

	n, err := CleanupNetworks(context.Background(), quietLogger(), projects, deploys, nets)
	if err != nil {
		t.Fatalf("CleanupNetworks: %v", err)
	}
	if n != 2 {
		t.Errorf("removed count: got %d, want 2", n)
	}
	sort.Strings(nets.removed)
	want := []string{"cobalt-project-api-5", "cobalt-project-api-6"}
	if len(nets.removed) != len(want) {
		t.Fatalf("removed: %v, want %v", nets.removed, want)
	}
	for i := range want {
		if nets.removed[i] != want[i] {
			t.Errorf("removed[%d]: got %q, want %q", i, nets.removed[i], want[i])
		}
	}
}

func TestCleanupNetworks_NoActiveRemovesAll(t *testing.T) {
	t.Parallel()

	projects := &fakeProjectLister{projects: []store.Project{{ID: 1, Name: "api"}}}
	deploys := &fakeDeployLister{byProject: map[int64][]int{1: nil}}
	nets := &fakeNetworkOps{
		byProject: map[string][]docker.NetworkInfo{
			"api": {
				{Name: "cobalt-project-api-1", DeploymentNumber: 1},
				{Name: "cobalt-project-api-2", DeploymentNumber: 2},
			},
		},
	}
	n, err := CleanupNetworks(context.Background(), quietLogger(), projects, deploys, nets)
	if err != nil {
		t.Fatalf("CleanupNetworks: %v", err)
	}
	if n != 2 {
		t.Errorf("removed: got %d, want 2", n)
	}
}

// Scenario the migration is supposed to cure on prod: 150 stale networks
// across many projects with only the latest deployment active. Verifies
// we slot multiple projects' nets in a single sweep and don't double-count.
func TestCleanupNetworks_MultiProjectMixedActive(t *testing.T) {
	t.Parallel()

	projects := &fakeProjectLister{
		projects: []store.Project{
			{ID: 1, Name: "api"},
			{ID: 2, Name: "next"},
			{ID: 3, Name: "dev-api"},
		},
	}
	deploys := &fakeDeployLister{
		byProject: map[int64][]int{
			1: {170},      // api: only newest active
			2: {769},      // next: only newest active
			3: {502, 503}, // dev-api: 2 active (e.g. mid-deploy)
		},
	}
	nets := &fakeNetworkOps{
		byProject: map[string][]docker.NetworkInfo{
			"api": {
				{Name: "cobalt-project-api-168", DeploymentNumber: 168},
				{Name: "cobalt-project-api-169", DeploymentNumber: 169},
				{Name: "cobalt-project-api-170", DeploymentNumber: 170}, // active
			},
			"next": {
				{Name: "cobalt-project-next-767", DeploymentNumber: 767},
				{Name: "cobalt-project-next-768", DeploymentNumber: 768},
				{Name: "cobalt-project-next-769", DeploymentNumber: 769}, // active
			},
			"dev-api": {
				{Name: "cobalt-project-dev-api-500", DeploymentNumber: 500},
				{Name: "cobalt-project-dev-api-501", DeploymentNumber: 501},
				{Name: "cobalt-project-dev-api-502", DeploymentNumber: 502}, // active
				{Name: "cobalt-project-dev-api-503", DeploymentNumber: 503}, // active
			},
		},
	}

	n, err := CleanupNetworks(context.Background(), quietLogger(), projects, deploys, nets)
	if err != nil {
		t.Fatalf("CleanupNetworks: %v", err)
	}
	// 2 (api) + 2 (next) + 2 (dev-api) = 6
	if n != 6 {
		t.Errorf("removed: got %d, want 6", n)
	}
	for _, name := range []string{
		"cobalt-project-api-170",
		"cobalt-project-next-769",
		"cobalt-project-dev-api-502",
		"cobalt-project-dev-api-503",
	} {
		for _, removed := range nets.removed {
			if removed == name {
				t.Errorf("removed an active network: %q", name)
			}
		}
	}
}

func TestCleanupNetworks_PerProjectFailureSkipsNotHalts(t *testing.T) {
	t.Parallel()

	projects := &fakeProjectLister{
		projects: []store.Project{{ID: 1, Name: "api"}, {ID: 2, Name: "web"}},
	}
	// API fails to list deploys; web succeeds.
	deploys := &errOnFirstDeployLister{
		first:        1,
		err:          errors.New("transient"),
		fallthrough_: &fakeDeployLister{byProject: map[int64][]int{2: {3}}},
	}
	nets := &fakeNetworkOps{
		byProject: map[string][]docker.NetworkInfo{
			"web": {
				{Name: "cobalt-project-web-3", DeploymentNumber: 3}, // active
				{Name: "cobalt-project-web-2", DeploymentNumber: 2}, // remove
			},
		},
	}

	n, err := CleanupNetworks(context.Background(), quietLogger(), projects, deploys, nets)
	if err != nil {
		t.Fatalf("CleanupNetworks should not fail: %v", err)
	}
	if n != 1 {
		t.Errorf("removed: got %d, want 1", n)
	}
	if len(nets.removed) != 1 || nets.removed[0] != "cobalt-project-web-2" {
		t.Errorf("removed: %v", nets.removed)
	}
}

func TestCleanupNetworks_ListNetworksFailureSkipsNotHalts(t *testing.T) {
	t.Parallel()

	projects := &fakeProjectLister{
		projects: []store.Project{{ID: 1, Name: "api"}, {ID: 2, Name: "web"}},
	}
	deploys := &fakeDeployLister{
		byProject: map[int64][]int{1: {1}, 2: {3}},
	}
	nets := &fakeNetworkOps{
		listErr: errors.New("docker daemon down"),
	}

	n, err := CleanupNetworks(context.Background(), quietLogger(), projects, deploys, nets)
	if err != nil {
		t.Fatalf("CleanupNetworks should not fail at top level: %v", err)
	}
	if n != 0 {
		t.Errorf("removed: got %d, want 0", n)
	}
}

// The expected race: a deploy is starting up while the sweep runs; docker
// refuses `network rm` because endpoints are attached. We log-and-continue.
// Next sweep catches it once the deploy moves on.
func TestCleanupNetworks_ActiveEndpointsErrorContinues(t *testing.T) {
	t.Parallel()

	projects := &fakeProjectLister{projects: []store.Project{{ID: 1, Name: "api"}}}
	deploys := &fakeDeployLister{byProject: map[int64][]int{1: nil}}
	nets := &fakeNetworkOps{
		byProject: map[string][]docker.NetworkInfo{
			"api": {
				{Name: "cobalt-project-api-1", DeploymentNumber: 1},
				{Name: "cobalt-project-api-2", DeploymentNumber: 2},
			},
		},
		removeErr: map[string]error{
			"cobalt-project-api-1": errors.New("network has active endpoints"),
		},
	}

	n, err := CleanupNetworks(context.Background(), quietLogger(), projects, deploys, nets)
	if err != nil {
		t.Errorf("err: %v", err)
	}
	// 1 succeeded, 1 stuck — sweep continues, count reflects success only
	if n != 1 {
		t.Errorf("removed: got %d, want 1", n)
	}
	if len(nets.removed) != 1 || nets.removed[0] != "cobalt-project-api-2" {
		t.Errorf("removed: %v", nets.removed)
	}
}

func TestCleanupNetworks_ProjectListErrorBubbles(t *testing.T) {
	t.Parallel()
	projects := &fakeProjectLister{err: errors.New("db down")}
	if _, err := CleanupNetworks(context.Background(), quietLogger(),
		projects, &fakeDeployLister{}, &fakeNetworkOps{}); err == nil {
		t.Error("expected error when project list fails")
	}
}

func TestCleanupNetworks_NoProjectsNoOp(t *testing.T) {
	t.Parallel()
	projects := &fakeProjectLister{projects: nil}
	n, err := CleanupNetworks(context.Background(), quietLogger(),
		projects, &fakeDeployLister{}, &fakeNetworkOps{})
	if err != nil {
		t.Errorf("err: %v", err)
	}
	if n != 0 {
		t.Errorf("removed: got %d, want 0", n)
	}
}
