package store

import (
	"context"
	"errors"
	"testing"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

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

func TestActiveDeploymentNumbers(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	pid, _ := db.CreateProject(ctx, Project{Name: "api", GithubRepo: "h/api", Branch: "main"})

	// Insert deployments in various statuses.
	for i, status := range []cobaltapi.State{
		cobaltapi.StateSuccess,  // keep image
		cobaltapi.StateBuilding, // keep image
		cobaltapi.StateFailed,   // not kept
		cobaltapi.StateCanceled, // not kept
		cobaltapi.StateQueued,   // keep image
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

	var status string
	var startedAt, finishedAt int64
	if err := db.QueryRow(
		`SELECT status, COALESCE(started_at, 0), COALESCE(finished_at, 0) FROM deployments WHERE id = ?`,
		id,
	).Scan(&status, &startedAt, &finishedAt); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if cobaltapi.State(status) != cobaltapi.StateSuccess {
		t.Errorf("status: %q", status)
	}
	if startedAt == 0 {
		t.Error("started_at not stamped on fetching transition")
	}
	if finishedAt == 0 {
		t.Error("finished_at not stamped on success transition")
	}
}

func TestDeleteExpiredPendingApps(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	// Insert three rows: two expired, one not.
	_, err := db.Exec(`
        INSERT INTO pending_github_apps (state, organization, created_at, expires_at)
        VALUES ('s1', 'o', unixepoch(), 100),
               ('s2', 'o', unixepoch(), 200),
               ('s3', 'o', unixepoch(), 1000000)
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

	var remaining int
	_ = db.QueryRow(`SELECT count(*) FROM pending_github_apps`).Scan(&remaining)
	if remaining != 1 {
		t.Errorf("remaining: got %d, want 1", remaining)
	}
}
