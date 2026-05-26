package store

import (
	"context"
	"testing"
)

func TestFindProjectsForRepoBranch(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	_, err := db.CreateProject(ctx, Project{
		Name: "api", GithubRepo: "heyblueteam/api", Branch: "main",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := db.FindProjectsForRepoBranch(ctx, "heyblueteam/api", "main")
	if err != nil {
		t.Fatalf("FindProjectsForRepoBranch (explicit main): %v", err)
	}
	if len(got) != 1 || got[0].Name != "api" {
		t.Errorf("explicit branch: got %v, want [api]", got)
	}

	got, err = db.FindProjectsForRepoBranch(ctx, "heyblueteam/api", "feat/x")
	if err != nil {
		t.Fatalf("FindProjectsForRepoBranch (feat/x): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("feat/x: got %v, want []", got)
	}
}

// TestFindProjectsForRepoBranch_RoundTripsPath pins that Project.Path
// is populated by this finder. Without it, the webhook handler reads
// every project with Path == "" and the path-filtered dispatch in
// handlePush silently degrades to "always deploy" — exactly the
// regression that motivated this test.
func TestFindProjectsForRepoBranch_RoundTripsPath(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	if _, err := db.CreateProject(ctx, Project{
		Name: "api", GithubRepo: "acme/monorepo", Branch: "main", Path: "services/api",
	}); err != nil {
		t.Fatalf("Create api: %v", err)
	}
	if _, err := db.CreateProject(ctx, Project{
		Name: "web", GithubRepo: "acme/monorepo", Branch: "main", Path: "web",
	}); err != nil {
		t.Fatalf("Create web: %v", err)
	}

	got, err := db.FindProjectsForRepoBranch(ctx, "acme/monorepo", "main")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d projects, want 2", len(got))
	}
	paths := map[string]string{}
	for _, p := range got {
		paths[p.Name] = p.Path
	}
	if paths["api"] != "services/api" {
		t.Errorf("api Path = %q, want services/api", paths["api"])
	}
	if paths["web"] != "web" {
		t.Errorf("web Path = %q, want web", paths["web"])
	}
}

func TestFindProjectsForRepoBranch_EmptyBranchDefaultsToMainMaster(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	// Project created with no explicit branch (empty string — matches "no branch set"
	// projects from older data or Disco imports).
	_, err := db.CreateProject(ctx, Project{
		Name: "api", GithubRepo: "heyblueteam/api", Branch: "",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Empty-branch project matches pushes to main.
	got, err := db.FindProjectsForRepoBranch(ctx, "heyblueteam/api", "main")
	if err != nil {
		t.Fatalf("FindProjectsForRepoBranch (empty → main): %v", err)
	}
	if len(got) != 1 || got[0].Name != "api" {
		t.Errorf("empty branch → main: got %v, want [api]", got)
	}

	// Empty-branch project matches pushes to master.
	got, err = db.FindProjectsForRepoBranch(ctx, "heyblueteam/api", "master")
	if err != nil {
		t.Fatalf("FindProjectsForRepoBranch (empty → master): %v", err)
	}
	if len(got) != 1 || got[0].Name != "api" {
		t.Errorf("empty branch → master: got %v, want [api]", got)
	}

	// Empty-branch project does NOT match pushes to other branches.
	got, err = db.FindProjectsForRepoBranch(ctx, "heyblueteam/api", "feat/x")
	if err != nil {
		t.Fatalf("FindProjectsForRepoBranch (empty → feat/x): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty branch → feat/x: got %v, want []", got)
	}
}
