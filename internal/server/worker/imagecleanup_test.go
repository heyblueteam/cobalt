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

type fakeProjectLister struct {
	projects []store.Project
	err      error
}

func (f *fakeProjectLister) ListProjects(_ context.Context) ([]store.Project, error) {
	return f.projects, f.err
}

type fakeDeployLister struct {
	byProject       map[int64][]int
	successByProject map[int64][]int
	err             error
}

func (f *fakeDeployLister) ActiveDeploymentNumbers(_ context.Context, projectID int64) ([]int, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byProject[projectID], nil
}

func (f *fakeDeployLister) RecentSuccessfulDeploymentNumbers(_ context.Context, projectID int64, limit int) ([]int, error) {
	if f.err != nil {
		return nil, f.err
	}
	all := f.successByProject[projectID]
	if limit <= 0 || len(all) <= limit {
		return all, nil
	}
	return all[:limit], nil
}

type fakeImageOps struct {
	mu         sync.Mutex
	byProject  map[string][]docker.ImageInfo
	listErr    error
	removeErr  error
	removed    []string
}

func (f *fakeImageOps) ListInternalImages(_ context.Context, projectName string) ([]docker.ImageInfo, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.byProject[projectName], nil
}

func (f *fakeImageOps) RemoveImage(_ context.Context, tag string) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, tag)
	return nil
}

func TestCleanupImages_RemovesNonActiveTags(t *testing.T) {
	t.Parallel()

	projects := &fakeProjectLister{
		projects: []store.Project{{ID: 1, Name: "api"}},
	}
	deploys := &fakeDeployLister{
		byProject: map[int64][]int{1: {7, 8}}, // 7 and 8 are still active
	}
	images := &fakeImageOps{
		byProject: map[string][]docker.ImageInfo{
			"api": {
				{Tag: "cobalt/project-api-default:5", DeploymentNumber: 5},
				{Tag: "cobalt/project-api-default:7", DeploymentNumber: 7},
				{Tag: "cobalt/project-api-default:8", DeploymentNumber: 8},
				{Tag: "cobalt/project-api-default:6", DeploymentNumber: 6},
			},
		},
	}

	n, err := CleanupImages(context.Background(), quietLogger(), projects, deploys, images, 0)
	if err != nil {
		t.Fatalf("CleanupImages: %v", err)
	}
	if n != 2 {
		t.Errorf("removed count: got %d, want 2", n)
	}
	sort.Strings(images.removed)
	want := []string{
		"cobalt/project-api-default:5",
		"cobalt/project-api-default:6",
	}
	if len(images.removed) != len(want) {
		t.Fatalf("removed: %v, want %v", images.removed, want)
	}
	for i := range want {
		if images.removed[i] != want[i] {
			t.Errorf("removed[%d]: got %q, want %q", i, images.removed[i], want[i])
		}
	}
}

func TestCleanupImages_NoActiveKeepsNothing(t *testing.T) {
	t.Parallel()

	projects := &fakeProjectLister{projects: []store.Project{{ID: 1, Name: "api"}}}
	deploys := &fakeDeployLister{byProject: map[int64][]int{1: nil}}
	images := &fakeImageOps{
		byProject: map[string][]docker.ImageInfo{
			"api": {
				{Tag: "t1", DeploymentNumber: 1},
				{Tag: "t2", DeploymentNumber: 2},
			},
		},
	}
	n, err := CleanupImages(context.Background(), quietLogger(), projects, deploys, images, 0)
	if err != nil {
		t.Fatalf("CleanupImages: %v", err)
	}
	if n != 2 {
		t.Errorf("removed: %d, want 2", n)
	}
}

func TestCleanupImages_PerProjectFailureSkipsNotHalts(t *testing.T) {
	t.Parallel()

	projects := &fakeProjectLister{
		projects: []store.Project{{ID: 1, Name: "api"}, {ID: 2, Name: "web"}},
	}
	// API fails to list deploys; web succeeds.
	deploys := &fakeDeployLister{
		byProject: map[int64][]int{2: {3}},
	}
	deploysWithError := &errOnFirstDeployLister{
		first:   1,
		err:     errors.New("transient"),
		fallthrough_: deploys,
	}
	images := &fakeImageOps{
		byProject: map[string][]docker.ImageInfo{
			"web": {
				{Tag: "cobalt/project-web-default:3", DeploymentNumber: 3}, // active, keep
				{Tag: "cobalt/project-web-default:2", DeploymentNumber: 2}, // remove
			},
		},
	}

	n, err := CleanupImages(context.Background(), quietLogger(), projects, deploysWithError, images, 0)
	if err != nil {
		t.Fatalf("CleanupImages should not fail: %v", err)
	}
	if n != 1 {
		t.Errorf("removed: got %d, want 1", n)
	}
	if len(images.removed) != 1 || images.removed[0] != "cobalt/project-web-default:2" {
		t.Errorf("removed: %v", images.removed)
	}
}

// errOnFirstDeployLister returns err for the project with id=first; for
// any other id it delegates to fallthrough_.
type errOnFirstDeployLister struct {
	first        int64
	err          error
	fallthrough_ *fakeDeployLister
}

func (e *errOnFirstDeployLister) ActiveDeploymentNumbers(ctx context.Context, projectID int64) ([]int, error) {
	if projectID == e.first {
		return nil, e.err
	}
	return e.fallthrough_.ActiveDeploymentNumbers(ctx, projectID)
}

func (e *errOnFirstDeployLister) RecentSuccessfulDeploymentNumbers(ctx context.Context, projectID int64, limit int) ([]int, error) {
	if projectID == e.first {
		return nil, e.err
	}
	return e.fallthrough_.RecentSuccessfulDeploymentNumbers(ctx, projectID, limit)
}

func TestCleanupImages_RemoveErrorContinues(t *testing.T) {
	t.Parallel()

	projects := &fakeProjectLister{projects: []store.Project{{ID: 1, Name: "api"}}}
	deploys := &fakeDeployLister{byProject: map[int64][]int{1: nil}}
	images := &fakeImageOps{
		byProject: map[string][]docker.ImageInfo{
			"api": {
				{Tag: "t1", DeploymentNumber: 1},
			},
		},
		removeErr: errors.New("image in use"),
	}

	n, err := CleanupImages(context.Background(), quietLogger(), projects, deploys, images, 0)
	if err != nil {
		t.Errorf("err: %v", err)
	}
	if n != 0 {
		t.Errorf("removed: got %d, want 0 (failure should not count)", n)
	}
}

// TestCleanupImages_KeepsRollbackRetentionWindow asserts that images
// belonging to the last K successful (but no-longer-active)
// deployments are preserved for `cobalt rollback`.
func TestCleanupImages_KeepsRollbackRetentionWindow(t *testing.T) {
	t.Parallel()
	projects := &fakeProjectLister{
		projects: []store.Project{{ID: 1, Name: "api"}},
	}
	deploys := &fakeDeployLister{
		// Only #10 is active (current live).
		byProject: map[int64][]int{1: {10}},
		// Successful history: 10, 9, 8, 7, 6 — rollback retention=3
		// keeps the top 3: 10, 9, 8.
		successByProject: map[int64][]int{1: {10, 9, 8, 7, 6}},
	}
	images := &fakeImageOps{
		byProject: map[string][]docker.ImageInfo{
			"api": {
				{Tag: "cobalt/project-api-default:10", DeploymentNumber: 10},
				{Tag: "cobalt/project-api-default:9", DeploymentNumber: 9},
				{Tag: "cobalt/project-api-default:8", DeploymentNumber: 8},
				{Tag: "cobalt/project-api-default:7", DeploymentNumber: 7},
				{Tag: "cobalt/project-api-default:6", DeploymentNumber: 6},
			},
		},
	}

	n, err := CleanupImages(context.Background(), quietLogger(), projects, deploys, images, 3)
	if err != nil {
		t.Fatalf("CleanupImages: %v", err)
	}
	if n != 2 {
		t.Errorf("removed: got %d, want 2 (only 7, 6)", n)
	}
	sort.Strings(images.removed)
	want := []string{"cobalt/project-api-default:6", "cobalt/project-api-default:7"}
	if len(images.removed) != 2 || images.removed[0] != want[0] || images.removed[1] != want[1] {
		t.Errorf("removed tags: got %v, want %v", images.removed, want)
	}
}

func TestCleanupImages_ProjectListErrorBubbles(t *testing.T) {
	t.Parallel()
	projects := &fakeProjectLister{err: errors.New("db down")}
	if _, err := CleanupImages(context.Background(), quietLogger(),
		projects, &fakeDeployLister{}, &fakeImageOps{}, 0); err == nil {
		t.Error("expected error when project list fails")
	}
}
