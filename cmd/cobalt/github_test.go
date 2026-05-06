package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func TestGithubAppsList(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond([]cobaltapi.GithubApp{
		{ID: 1, AppID: 100, Slug: "cobalt-ci", Owner: "heyblueteam", HTMLURL: "https://github.com/apps/cobalt-ci"},
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"github", "apps", "list"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(buf.String(), "cobalt-ci") {
		t.Errorf("missing app: %s", buf.String())
	}
}

func TestGithubAppsListJSON(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond([]cobaltapi.GithubApp{})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"github", "apps", "list", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("expected JSON output")
	}
}

func TestGithubAppsAddNonInteractive(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond(&cobaltapi.PendingAppCreateResponse{
		ID: 1, URL: "https://github.com/apps/cobalt/installations/new", ExpiresAt: 1700000000,
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"github", "apps", "add", "--organization", "heyblueteam", "--non-interactive"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(buf.String(), "URL:") {
		t.Errorf("expected URL: %s", buf.String())
	}
}

func TestGithubAppsManage(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond([]cobaltapi.GithubApp{
		{ID: 1, Owner: "heyblueteam", HTMLURL: "https://github.com/apps/cobalt-ci"},
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"github", "apps", "manage", "heyblueteam"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(buf.String(), "https://github.com/apps/cobalt-ci") {
		t.Errorf("missing htmlUrl: %s", buf.String())
	}
}

func TestGithubAppsManageNotFound(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond([]cobaltapi.GithubApp{})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"github", "apps", "manage", "unknown-org"})
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error for unknown org")
	}
}

func TestGithubAppsPrune(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond(&cobaltapi.PruneResponse{
		AppsRemoved: 1, InstallationsRemoved: 2, ReposAdded: 3, ReposRemoved: 4,
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"github", "apps", "prune"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(buf.String(), "1") || !contains(buf.String(), "3") {
		t.Errorf("missing prune summary: %s", buf.String())
	}
}

func TestGithubReposList(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond([]cobaltapi.GithubAppRepo{
		{ID: 1, InstallationID: 100, FullName: "heyblueteam/api", DefaultBranch: "main"},
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"github", "repos"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(buf.String(), "heyblueteam/api") {
		t.Errorf("missing repo: %s", buf.String())
	}
}
