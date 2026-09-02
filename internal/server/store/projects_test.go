package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func TestCreateAndGetProject(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	id, err := db.CreateProject(ctx, Project{
		Name: "api", GithubRepo: "heyblueteam/api", Branch: "main",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == 0 {
		t.Fatal("id: got 0")
	}

	got, err := db.GetProjectByName(ctx, "api")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if got.ID != id || got.Name != "api" || got.GithubRepo != "heyblueteam/api" {
		t.Errorf("got %+v", got)
	}
}

func TestGetProjectByName_NotFound(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	_, err := db.GetProjectByName(context.Background(), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestListProjects_OrderedByName(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	for _, n := range []string{"web", "api", "redis"} {
		_, _ = db.CreateProject(ctx, Project{
			Name: n, GithubRepo: "heyblueteam/" + n, Branch: "main",
		})
	}
	got, err := db.ListProjects(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"api", "redis", "web"}
	if len(got) != len(want) {
		t.Fatalf("len: %d", len(got))
	}
	for i := range got {
		if got[i].Name != want[i] {
			t.Errorf("[%d]: got %q, want %q", i, got[i].Name, want[i])
		}
	}
}

func TestRenameProject(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	id, _ := db.CreateProject(ctx, Project{Name: "old", GithubRepo: "h/o", Branch: "main"})
	if err := db.RenameProject(ctx, id, "new"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	got, err := db.GetProjectByName(ctx, "new")
	if err != nil {
		t.Fatalf("GetByName new: %v", err)
	}
	if got.ID != id {
		t.Errorf("ID drift: got %d, want %d", got.ID, id)
	}
	if _, err := db.GetProjectByName(ctx, "old"); !errors.Is(err, ErrNotFound) {
		t.Error("old name still resolves")
	}
}

func TestCreateProject_NameTaken(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	_, err := db.CreateProject(ctx, Project{Name: "api", GithubRepo: "h/api", Branch: "main"})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err = db.CreateProject(ctx, Project{Name: "api", GithubRepo: "h/api2", Branch: "main"})
	if !errors.Is(err, ErrProjectNameTaken) {
		t.Errorf("got %v, want ErrProjectNameTaken", err)
	}
}

func TestDeleteProject(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	id, _ := db.CreateProject(ctx, Project{Name: "x", GithubRepo: "h/x", Branch: "main"})
	if err := db.DeleteProject(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := db.DeleteProject(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: got %v, want ErrNotFound", err)
	}
}

// TestProject_PathRoundTrip proves the optional repo sub-path stored at
// create time round-trips through every retrieval path (GetByID,
// GetByName, ListProjects). All four queries SELECT projects use the
// same scanProjectRow helper, but the column order is independently
// declared in each SELECT — drift between them would be caught here.
func TestProject_PathRoundTrip(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	id, err := db.CreateProject(ctx, Project{
		Name: "monorepo-api", GithubRepo: "acme/monorepo", Branch: "main",
		Path: "services/api",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	t.Run("GetByID", func(t *testing.T) {
		p, err := db.GetProjectByID(ctx, id)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if p.Path != "services/api" {
			t.Errorf("Path = %q, want %q", p.Path, "services/api")
		}
	})

	t.Run("GetByName", func(t *testing.T) {
		p, err := db.GetProjectByName(ctx, "monorepo-api")
		if err != nil {
			t.Fatalf("GetByName: %v", err)
		}
		if p.Path != "services/api" {
			t.Errorf("Path = %q, want %q", p.Path, "services/api")
		}
	})

	t.Run("ListProjects", func(t *testing.T) {
		all, err := db.ListProjects(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		var found *Project
		for i := range all {
			if all[i].ID == id {
				found = &all[i]
				break
			}
		}
		if found == nil {
			t.Fatal("created project not in list")
		}
		if found.Path != "services/api" {
			t.Errorf("Path = %q, want %q", found.Path, "services/api")
		}
	})
}

// TestProject_EmptyPathIsRepoRoot proves the default — every existing
// project (created before this column existed, or by callers that don't
// set Path) sees an empty string back. The migration's DEFAULT ” on
// the column is what makes this work; this test catches accidental
// regression to NULL or a bogus default.
func TestProject_EmptyPathIsRepoRoot(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	id, err := db.CreateProject(ctx, Project{
		Name: "root-project", GithubRepo: "acme/root", Branch: "main",
		// Path intentionally omitted
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	p, err := db.GetProjectByID(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.Path != "" {
		t.Errorf("Path = %q, want empty (repo root)", p.Path)
	}
}

// TestCreateProject_RejectsInvalidPath ensures path validation runs at
// the store boundary, not just at the API layer. A direct
// store.CreateProject call (e.g. from an internal job, a future
// import command, a migration script) gets the same shape rules.
func TestCreateProject_RejectsInvalidPath(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	_, err := db.CreateProject(ctx, Project{
		Name: "bad", GithubRepo: "acme/x", Branch: "main",
		Path: "/absolute",
	})
	if err == nil {
		t.Fatal("expected error for absolute path")
	}
}

// TestOtherProjectsWithSameSource_Solo proves a project with no siblings
// returns 0 — the deploy builder will route through the shared builder.
func TestOtherProjectsWithSameSource_Solo(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	id, _ := db.CreateProject(ctx, Project{
		Name: "solo", GithubRepo: "heyblueteam/blue", Branch: "main", Path: "api",
	})
	n, err := db.OtherProjectsWithSameSource(ctx, id)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 0 {
		t.Errorf("count: got %d, want 0", n)
	}
}

// TestOtherProjectsWithSameSource_Sibling proves two projects with the
// same (repo, branch, path) each see the other.
func TestOtherProjectsWithSameSource_Sibling(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	idA, _ := db.CreateProject(ctx, Project{
		Name: "next", GithubRepo: "heyblueteam/blue", Branch: "main", Path: "app",
	})
	idB, _ := db.CreateProject(ctx, Project{
		Name: "white-label", GithubRepo: "heyblueteam/blue", Branch: "main", Path: "app",
	})

	for _, c := range []struct {
		id   int64
		want int
	}{{idA, 1}, {idB, 1}} {
		n, err := db.OtherProjectsWithSameSource(ctx, c.id)
		if err != nil {
			t.Fatalf("query id=%d: %v", c.id, err)
		}
		if n != c.want {
			t.Errorf("id=%d count: got %d, want %d", c.id, n, c.want)
		}
	}
}

// TestOtherProjectsWithSameSource_DifferentPath proves that two projects
// sharing repo + branch but pointing at different sub-paths do NOT count
// as siblings — they don't race on BuildKit's in-memory cache because
// the build contexts are disjoint.
func TestOtherProjectsWithSameSource_DifferentPath(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	idAPI, _ := db.CreateProject(ctx, Project{
		Name: "api", GithubRepo: "heyblueteam/blue", Branch: "main", Path: "api",
	})
	_, _ = db.CreateProject(ctx, Project{
		Name: "next", GithubRepo: "heyblueteam/blue", Branch: "main", Path: "app",
	})
	n, err := db.OtherProjectsWithSameSource(ctx, idAPI)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 0 {
		t.Errorf("count: got %d, want 0 (different path)", n)
	}
}

// TestOtherProjectsWithSameSource_DifferentBranch proves the branch
// column participates in the match.
func TestOtherProjectsWithSameSource_DifferentBranch(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	idA, _ := db.CreateProject(ctx, Project{
		Name: "prod", GithubRepo: "heyblueteam/blue", Branch: "main", Path: "api",
	})
	_, _ = db.CreateProject(ctx, Project{
		Name: "staging", GithubRepo: "heyblueteam/blue", Branch: "staging", Path: "api",
	})
	n, err := db.OtherProjectsWithSameSource(ctx, idA)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 0 {
		t.Errorf("count: got %d, want 0 (different branch)", n)
	}
}

// TestOtherProjectsWithSameSource_AfterUpdate proves the live-query
// model — when one project's source changes via UpdateProjectSource so
// it now matches another, both projects start reporting siblings without
// any per-project state migration.
func TestOtherProjectsWithSameSource_AfterUpdate(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	idA, _ := db.CreateProject(ctx, Project{
		Name: "a", GithubRepo: "heyblueteam/blue", Branch: "main", Path: "api",
	})
	idB, _ := db.CreateProject(ctx, Project{
		Name: "b", GithubRepo: "heyblueteam/blue", Branch: "main", Path: "app",
	})

	// Before update: different paths → no siblings.
	if n, _ := db.OtherProjectsWithSameSource(ctx, idA); n != 0 {
		t.Fatalf("before update, A: got %d, want 0", n)
	}

	// Retarget A to share the path with B.
	if err := db.UpdateProjectSource(ctx, idA, "heyblueteam/blue", "main", "app", ""); err != nil {
		t.Fatalf("UpdateProjectSource: %v", err)
	}

	for _, c := range []struct {
		id   int64
		want int
	}{{idA, 1}, {idB, 1}} {
		n, err := db.OtherProjectsWithSameSource(ctx, c.id)
		if err != nil {
			t.Fatalf("query id=%d: %v", c.id, err)
		}
		if n != c.want {
			t.Errorf("after update id=%d: got %d, want %d", c.id, n, c.want)
		}
	}
}

// TestOtherProjectsWithSameSource_EmptyPath proves two projects sharing
// repo + branch with path="" (repo-root deploys — the pre-0006-migration
// shape, still the default) match as siblings. Guards against a future
// schema change that makes path nullable: SQL tuple IN treats NULL as
// non-equal to NULL, which would silently break sibling detection for
// repo-root projects.
func TestOtherProjectsWithSameSource_EmptyPath(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	idA, _ := db.CreateProject(ctx, Project{
		Name: "root-a", GithubRepo: "heyblueteam/blue", Branch: "main", Path: "",
	})
	idB, _ := db.CreateProject(ctx, Project{
		Name: "root-b", GithubRepo: "heyblueteam/blue", Branch: "main", Path: "",
	})

	for _, c := range []struct {
		id   int64
		want int
	}{{idA, 1}, {idB, 1}} {
		n, err := db.OtherProjectsWithSameSource(ctx, c.id)
		if err != nil {
			t.Fatalf("query id=%d: %v", c.id, err)
		}
		if n != c.want {
			t.Errorf("id=%d count: got %d, want %d (empty-path siblings must match)", c.id, n, c.want)
		}
	}
}

// TestOtherProjectsWithSameSource_NonExistent proves a lookup against an
// unknown ID returns (0, nil) — the caller (deploy builder) treats that
// as "no siblings, use shared builder". Defensive: avoids surfacing a
// transient race between project delete and an in-flight deploy as a
// deploy-fatal error.
func TestOtherProjectsWithSameSource_NonExistent(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	n, err := db.OtherProjectsWithSameSource(context.Background(), 99999)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 0 {
		t.Errorf("count: got %d, want 0", n)
	}
}

func TestActiveDeploymentNumbers(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	pid, _ := db.CreateProject(ctx, Project{Name: "api", GithubRepo: "h/api", Branch: "main"})

	for i, status := range []cobaltapi.State{
		cobaltapi.StateSuccess,
		cobaltapi.StateSuccess,
		cobaltapi.StateBuilding,
		cobaltapi.StateFailed,
		cobaltapi.StateCanceled,
		cobaltapi.StateQueued,
	} {
		_, err := db.CreateDeployment(ctx, Deployment{
			ProjectID: pid, Number: i + 1, Status: status,
		})
		if err != nil {
			t.Fatalf("CreateDeployment %d: %v", i, err)
		}
	}

	got, err := db.ActiveDeploymentNumbers(ctx, pid)
	if err != nil {
		t.Fatalf("ActiveDeploymentNumbers: %v", err)
	}
	want := []int{2, 3, 6}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v (in-flight plus latest success)", got, want)
	}
}

func TestSetDeploymentStatus(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	pid, _ := db.CreateProject(ctx, Project{Name: "api", GithubRepo: "h/api", Branch: "main"})
	id, _ := db.CreateDeployment(ctx, Deployment{
		ProjectID: pid, Number: 1, Status: cobaltapi.StateQueued,
	})

	if err := db.SetDeploymentStatus(ctx, id, cobaltapi.StateFetching); err != nil {
		t.Fatalf("set fetching: %v", err)
	}
	if err := db.SetDeploymentStatus(ctx, id, cobaltapi.StateSuccess); err != nil {
		t.Fatalf("set success: %v", err)
	}

	got, err := db.GetDeployment(ctx, id)
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if got.Status != cobaltapi.StateSuccess {
		t.Errorf("status: %q", got.Status)
	}
	if got.StartedAt == nil {
		t.Error("started_at not stamped on fetching transition")
	}
	if got.FinishedAt == nil {
		t.Error("finished_at not stamped on success transition")
	}
}

func TestDeleteExpiredPendingApps(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	_, err := db.ExecuteSingle(ctx, `
        INSERT INTO pending_github_apps (state, organization, created_at, expires_at)
        VALUES ('s1', 'o', strftime('%s', 'now'), 100),
               ('s2', 'o', strftime('%s', 'now'), 200),
               ('s3', 'o', strftime('%s', 'now'), 1000000)
    `)
	if err != nil {
		t.Fatal(err)
	}

	n, err := db.DeleteExpiredPendingApps(ctx, 500)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted: got %d, want 2", n)
	}
}

// TestUpdateProjectSource_RoundTrip proves a project can be retargeted to a
// different repo/branch/path in place and that the new values surface via
// every retrieval path (GetByID + GetByName).
func TestUpdateProjectSource_RoundTrip(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	id, err := db.CreateProject(ctx, Project{
		Name: "api", GithubRepo: "heyblueteam/api", Branch: "main",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := db.UpdateProjectSource(ctx, id, "heyblueteam/blue", "develop", "api", ""); err != nil {
		t.Fatalf("UpdateProjectSource: %v", err)
	}

	byID, err := db.GetProjectByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if byID.GithubRepo != "heyblueteam/blue" {
		t.Errorf("GithubRepo: got %q, want heyblueteam/blue", byID.GithubRepo)
	}
	if byID.Branch != "develop" {
		t.Errorf("Branch: got %q, want develop", byID.Branch)
	}
	if byID.Path != "api" {
		t.Errorf("Path: got %q, want api", byID.Path)
	}

	byName, err := db.GetProjectByName(ctx, "api")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if byName.GithubRepo != "heyblueteam/blue" || byName.Path != "api" {
		t.Errorf("GetByName drift: %+v", byName)
	}
}

// TestUpdateProjectSource_NotFound proves updating a non-existent project
// returns ErrNotFound — the API handler uses this to map to HTTP 404.
func TestUpdateProjectSource_NotFound(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	err := db.UpdateProjectSource(context.Background(), 99999, "heyblueteam/blue", "main", "api", "")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

// TestUpdateProjectSource_InvalidPath proves the store-layer path validator
// rejects bad paths even when the API handler validator is skipped.
func TestUpdateProjectSource_InvalidPath(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	id, _ := db.CreateProject(ctx, Project{Name: "x", GithubRepo: "h/x", Branch: "main"})

	// Leading slash is rejected by ValidateProjectPath.
	if err := db.UpdateProjectSource(ctx, id, "h/x", "main", "/api", ""); err == nil {
		t.Error("UpdateProjectSource accepted leading-slash path, want validation error")
	}
	// `..` is rejected.
	if err := db.UpdateProjectSource(ctx, id, "h/x", "main", "../escape", ""); err == nil {
		t.Error("UpdateProjectSource accepted parent-traversal path, want validation error")
	}
}

// TestUpdateProjectSource_IsolatesProjects proves updating one project's
// source does not leak into other projects' rows — the WHERE clause is
// correctly id-scoped.
func TestUpdateProjectSource_IsolatesProjects(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	idA, _ := db.CreateProject(ctx, Project{Name: "a", GithubRepo: "h/a", Branch: "main"})
	idB, _ := db.CreateProject(ctx, Project{Name: "b", GithubRepo: "h/b", Branch: "main"})

	if err := db.UpdateProjectSource(ctx, idA, "h/mono", "develop", "services/a", ""); err != nil {
		t.Fatalf("UpdateProjectSource a: %v", err)
	}

	gotB, err := db.GetProjectByID(ctx, idB)
	if err != nil {
		t.Fatalf("GetByID b: %v", err)
	}
	if gotB.GithubRepo != "h/b" || gotB.Branch != "main" || gotB.Path != "" {
		t.Errorf("project b leaked changes from a: %+v", gotB)
	}
}

// TestUpdateProjectSource_EmptyPathClearsSubdir proves callers can move a
// project back to repo-root by passing an empty path. Important for the
// inverse of the monorepo cutover (un-monorepo-ing).
func TestUpdateProjectSource_EmptyPathClearsSubdir(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	id, _ := db.CreateProject(ctx, Project{
		Name: "x", GithubRepo: "h/mono", Branch: "main", Path: "services/x",
	})

	if err := db.UpdateProjectSource(ctx, id, "h/x", "main", "", ""); err != nil {
		t.Fatalf("UpdateProjectSource: %v", err)
	}
	got, err := db.GetProjectByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Path != "" {
		t.Errorf("Path: got %q, want empty", got.Path)
	}
	if got.GithubRepo != "h/x" {
		t.Errorf("GithubRepo: got %q, want h/x", got.GithubRepo)
	}
}

// TestUpdateProjectSource_BumpsUpdatedAt proves the updated_at column ticks
// forward on each update — used by external observers (UI, audit logs) to
// detect that a config change happened even when the row's other fields
// happen to round-trip to the same values.
func TestUpdateProjectSource_BumpsUpdatedAt(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	id, _ := db.CreateProject(ctx, Project{Name: "x", GithubRepo: "h/x", Branch: "main"})

	before, err := db.GetProjectByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID before: %v", err)
	}

	// sleep 1.1s so strftime('%s') ticks at least once (second resolution).
	time.Sleep(1100 * time.Millisecond)

	if err := db.UpdateProjectSource(ctx, id, "h/x", "main", "", ""); err != nil {
		t.Fatalf("UpdateProjectSource: %v", err)
	}
	after, err := db.GetProjectByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID after: %v", err)
	}
	if after.UpdatedAt <= before.UpdatedAt {
		t.Errorf("UpdatedAt did not advance: before=%d after=%d", before.UpdatedAt, after.UpdatedAt)
	}
}

// TestUpdateProjectSource_WatchPaths proves watch_paths round-trips
// through update + read, that WatchPathsList tolerates spacing and
// stray commas, and that invalid entries are rejected at the store
// boundary like path is.
func TestUpdateProjectSource_WatchPaths(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	id, _ := db.CreateProject(ctx, Project{
		Name: "api", GithubRepo: "heyblueteam/blue", Branch: "main", Path: "api",
	})

	if err := db.UpdateProjectSource(ctx, id, "heyblueteam/blue", "main", "api", "shared, packages/ui"); err != nil {
		t.Fatalf("UpdateProjectSource with watchPaths: %v", err)
	}
	p, err := db.GetProjectByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if p.WatchPaths != "shared, packages/ui" {
		t.Errorf("WatchPaths: %q", p.WatchPaths)
	}
	got := p.WatchPathsList()
	if len(got) != 2 || got[0] != "shared" || got[1] != "packages/ui" {
		t.Errorf("WatchPathsList: %#v, want [shared packages/ui]", got)
	}

	// Clearing works.
	if err := db.UpdateProjectSource(ctx, id, "heyblueteam/blue", "main", "api", ""); err != nil {
		t.Fatalf("clear watchPaths: %v", err)
	}
	p, _ = db.GetProjectByID(ctx, id)
	if p.WatchPaths != "" || p.WatchPathsList() != nil {
		t.Errorf("after clear: %q / %#v", p.WatchPaths, p.WatchPathsList())
	}

	// Invalid entries rejected.
	if err := db.UpdateProjectSource(ctx, id, "heyblueteam/blue", "main", "api", "../escape"); err == nil {
		t.Error("accepted parent-traversal watch path, want validation error")
	}
}
