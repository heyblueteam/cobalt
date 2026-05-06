package store

import (
	"context"
	"errors"
	"testing"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func TestGetLastSuccessfulDeployment_NoneReturnsNotFound(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	pid, _ := db.CreateProject(context.Background(), Project{
		Name: "api", GithubRepo: "h/api", Branch: "main",
	})
	_, err := db.GetLastSuccessfulDeployment(context.Background(), pid)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestGetLastSuccessfulDeployment_HighestNumberWins(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	pid, _ := db.CreateProject(ctx, Project{Name: "api", GithubRepo: "h/api", Branch: "main"})

	// 3 deploys: success 1, success 2, success 3.
	for i := 1; i <= 3; i++ {
		id, err := db.CreateDeployment(ctx, Deployment{
			ProjectID: pid, Number: i, Status: cobaltapi.StateQueued,
		})
		if err != nil {
			t.Fatal(err)
		}
		_ = db.SetDeploymentStatus(ctx, id, cobaltapi.StateSuccess)
	}

	got, err := db.GetLastSuccessfulDeployment(ctx, pid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Number != 3 {
		t.Errorf("Number: %d, want 3", got.Number)
	}
}

func TestGetLastSuccessfulDeployment_IgnoresFailedAndQueued(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	pid, _ := db.CreateProject(ctx, Project{Name: "api", GithubRepo: "h/api", Branch: "main"})

	// Newest two are failed and queued; success was deployment #1.
	id1, _ := db.CreateDeployment(ctx, Deployment{ProjectID: pid, Number: 1, Status: cobaltapi.StateQueued})
	_ = db.SetDeploymentStatus(ctx, id1, cobaltapi.StateSuccess)
	id2, _ := db.CreateDeployment(ctx, Deployment{ProjectID: pid, Number: 2, Status: cobaltapi.StateQueued})
	_ = db.SetDeploymentStatus(ctx, id2, cobaltapi.StateFailed)
	_, _ = db.CreateDeployment(ctx, Deployment{ProjectID: pid, Number: 3, Status: cobaltapi.StateQueued})

	got, err := db.GetLastSuccessfulDeployment(ctx, pid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Number != 1 {
		t.Errorf("Number: %d, want 1", got.Number)
	}
}
