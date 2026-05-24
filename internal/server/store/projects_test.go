package store

import (
	"context"
	"errors"
	"testing"

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
// set Path) sees an empty string back. The migration's DEFAULT '' on
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

func TestActiveDeploymentNumbers(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	pid, _ := db.CreateProject(ctx, Project{Name: "api", GithubRepo: "h/api", Branch: "main"})

	for i, status := range []cobaltapi.State{
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
	if len(got) != 3 {
		t.Errorf("got %v, want 3 image-keeping deployments", got)
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
