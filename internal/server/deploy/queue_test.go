package deploy

import (
	"context"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func openTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newProject(t *testing.T, db *store.DB, name string) int64 {
	t.Helper()
	id, err := db.CreateProject(context.Background(), store.Project{
		Name: name, GithubRepo: "h/" + name, Branch: "main",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return id
}

func TestQueue_EnqueueAssignsMonotonicNumber(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	q := NewQueue(db)
	pid := newProject(t, db, "api")
	ctx := context.Background()

	id1, n1, err := q.Enqueue(ctx, EnqueueRequest{ProjectID: pid})
	if err != nil {
		t.Fatalf("Enqueue 1: %v", err)
	}
	id2, n2, err := q.Enqueue(ctx, EnqueueRequest{ProjectID: pid})
	if err != nil {
		t.Fatalf("Enqueue 2: %v", err)
	}
	if n1 != 1 || n2 != 2 {
		t.Errorf("numbers: got %d, %d; want 1, 2", n1, n2)
	}
	if id1 == id2 {
		t.Errorf("ids collided")
	}
}

func TestQueue_EnqueueDifferentProjectsIndependent(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	q := NewQueue(db)
	api := newProject(t, db, "api")
	web := newProject(t, db, "web")

	_, n1, _ := q.Enqueue(context.Background(), EnqueueRequest{ProjectID: api})
	_, n2, _ := q.Enqueue(context.Background(), EnqueueRequest{ProjectID: web})
	if n1 != 1 || n2 != 1 {
		t.Errorf("numbers per project: got %d, %d; want 1, 1", n1, n2)
	}
}

func TestQueue_EnqueuePreservesOptionalFields(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	q := NewQueue(db)
	pid := newProject(t, db, "api")

	id, _, err := q.Enqueue(context.Background(), EnqueueRequest{
		ProjectID: pid,
		CommitSHA: "abc123",
		NoCache:   true,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	dep, err := db.GetDeployment(context.Background(), id)
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if !dep.CommitSHA.Valid || dep.CommitSHA.String != "abc123" {
		t.Errorf("commit_sha: %+v", dep.CommitSHA)
	}
	if !dep.NoCache {
		t.Error("no_cache: false, want true")
	}
	if dep.Status != cobaltapi.StateQueued {
		t.Errorf("status: %q", dep.Status)
	}
}

func TestQueue_CancelQueued(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	q := NewQueue(db)
	pid := newProject(t, db, "api")
	id, _, _ := q.Enqueue(context.Background(), EnqueueRequest{ProjectID: pid})

	cancelInFlight, err := q.Cancel(context.Background(), id)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if cancelInFlight {
		t.Error("cancelInFlight: true for queued row")
	}
	dep, _ := db.GetDeployment(context.Background(), id)
	if dep.Status != cobaltapi.StateCanceled {
		t.Errorf("status: %q, want canceled", dep.Status)
	}
}

func TestQueue_CancelActiveSignalsCaller(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	q := NewQueue(db)
	pid := newProject(t, db, "api")
	id, _, _ := q.Enqueue(context.Background(), EnqueueRequest{ProjectID: pid})
	_ = db.SetDeploymentStatus(context.Background(), id, cobaltapi.StateBuilding)

	cancelInFlight, err := q.Cancel(context.Background(), id)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if !cancelInFlight {
		t.Error("cancelInFlight: false for active deploy")
	}
	// Status should NOT have changed yet — the dispatcher writes it.
	dep, _ := db.GetDeployment(context.Background(), id)
	if dep.Status != cobaltapi.StateBuilding {
		t.Errorf("status: %q, want building (unchanged)", dep.Status)
	}
}

func TestQueue_CancelTerminalRejected(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	q := NewQueue(db)
	pid := newProject(t, db, "api")
	id, _, _ := q.Enqueue(context.Background(), EnqueueRequest{ProjectID: pid})
	_ = db.SetDeploymentStatus(context.Background(), id, cobaltapi.StateSuccess)

	if _, err := q.Cancel(context.Background(), id); err == nil {
		t.Error("expected error canceling terminal")
	}
}
