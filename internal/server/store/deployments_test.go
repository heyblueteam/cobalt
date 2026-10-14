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

func TestFirstSuccessfulDeploymentAfter(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	pid, err := db.CreateProject(ctx, Project{Name: "retire", GithubRepo: "h/retire", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	for i, status := range []cobaltapi.State{cobaltapi.StateSuccess, cobaltapi.StateFailed, cobaltapi.StateSuccess, cobaltapi.StateSuccess} {
		id, err := db.CreateDeployment(ctx, Deployment{ProjectID: pid, Number: i + 1, Status: cobaltapi.StateQueued})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.SetDeploymentStatus(ctx, id, status); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.FirstSuccessfulDeploymentAfter(ctx, pid, 1, 4)
	if err != nil || got.Number != 3 || got.FinishedAt == nil {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	for _, bounds := range [][2]int{{1, 2}, {4, 4}} {
		if _, err := db.FirstSuccessfulDeploymentAfter(ctx, pid, bounds[0], bounds[1]); !errors.Is(err, ErrNotFound) {
			t.Fatalf("bounds=%v err=%v", bounds, err)
		}
	}
	if _, err := db.FirstSuccessfulDeploymentAfter(ctx, pid+999, 1, 4); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
