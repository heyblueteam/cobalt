package store

import (
	"context"
	"errors"
	"testing"
)

func TestListDomainsForProject_Empty(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	pid, _ := db.CreateProject(context.Background(), Project{
		Name: "api", GithubRepo: "h/api", Branch: "main",
	})
	got, err := db.ListDomainsForProject(context.Background(), pid)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestListDomainsForProject_OrderedByID(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	pid, _ := db.CreateProject(context.Background(), Project{
		Name: "api", GithubRepo: "h/api", Branch: "main",
	})
	for _, n := range []string{"api.example.com", "alt.example.com", "z.example.com"} {
		if err := db.AddDomain(context.Background(), pid, n); err != nil {
			t.Fatalf("AddDomain: %v", err)
		}
	}
	got, err := db.ListDomainsForProject(context.Background(), pid)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"api.example.com", "alt.example.com", "z.example.com"}
	if len(got) != len(want) {
		t.Fatalf("len: %d", len(got))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRemoveDomain(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	pid, _ := db.CreateProject(context.Background(), Project{
		Name: "api", GithubRepo: "h/api", Branch: "main",
	})
	_ = db.AddDomain(context.Background(), pid, "example.com")

	if err := db.RemoveDomain(context.Background(), pid, "example.com"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := db.RemoveDomain(context.Background(), pid, "example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second remove: got %v, want ErrNotFound", err)
	}
}
