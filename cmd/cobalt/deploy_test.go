package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func TestDeploymentsList(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond([]cobaltapi.Deployment{
		{ID: 1, Number: 1, Status: cobaltapi.StateSuccess, CommitSHA: "abc1234xyz"},
		{ID: 2, Number: 2, Status: cobaltapi.StateFailed, CommitSHA: "def5678abc"},
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"deployments", "list", "--project", "api"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := buf.String()
	if !contains(got, "#1") || !contains(got, "success") {
		t.Errorf("missing deployment 1: %s", got)
	}
	if !contains(got, "#2") || !contains(got, "failed") {
		t.Errorf("missing deployment 2: %s", got)
	}
}

func TestDeploymentsListJSON(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond([]cobaltapi.Deployment{
		{ID: 1, Number: 1, Status: cobaltapi.StateSuccess},
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"deployments", "list", "--project", "api", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("expected JSON output")
	}
}

func TestDeploymentsListEmpty(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond([]cobaltapi.Deployment{})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"deployments", "list", "--project", "api"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(buf.String(), "No deployments") {
		t.Errorf("expected empty message: %s", buf.String())
	}
}

func TestDeploymentsListWithLimit(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond([]cobaltapi.Deployment{
		{ID: 1, Number: 1, Status: cobaltapi.StateSuccess},
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"deployments", "list", "--project", "api", "--limit", "10"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(api.lastQuery, "limit=10") {
		t.Errorf("query missing limit: %s", api.lastQuery)
	}
}

func TestDeploymentsCancel(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	var callCount int
	api.handler = func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			// GET /api/projects/undefined/deployments?limit=50 (finding in-flight)
			json.NewEncoder(w).Encode([]cobaltapi.Deployment{
				{ID: 42, Number: 7, Status: cobaltapi.StateSwapping},
			})
		} else {
			// POST /api/deployments/42/cancel
			w.WriteHeader(http.StatusOK)
		}
	}

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"deployments", "cancel", "--project", "api"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(buf.String(), "Canceled") {
		t.Errorf("expected 'Canceled': %s", buf.String())
	}
}

func TestDeploymentsCancelExplicit(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// GET /api/deployments/99
		json.NewEncoder(w).Encode(&cobaltapi.Deployment{ID: 99, Number: 5})
	}

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"deployments", "cancel", "--project", "api", "--deployment", "99"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
}

func TestDeploymentsOutputLatest(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	var callCount int
	api.handler = func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			// List recent deployments → return one
			json.NewEncoder(w).Encode([]cobaltapi.Deployment{
				{ID: 10, Number: 3, Status: cobaltapi.StateSuccess},
			})
		} else {
			// SSE output — just close immediately for test
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("data: build output line\n\n"))
			return
		}
	}

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"deployments", "output", "--project", "api"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(buf.String(), "build output line") {
		t.Errorf("missing output: %s", buf.String())
	}
}

func TestDeployNoFollow(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond(&cobaltapi.Deployment{
		ID: 1, Number: 1, Status: cobaltapi.StateQueued,
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"deploy", "--project", "api", "--no-follow"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := buf.String()
	if !contains(got, "#1") {
		t.Errorf("expected deployment info: %s", got)
	}
}

func TestDeployNoFollowJSON(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond(&cobaltapi.Deployment{
		ID: 1, Number: 1, Status: cobaltapi.StateQueued,
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"deploy", "--project", "api", "--no-follow", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("expected JSON output")
	}
}

func TestDeployWithCommit(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond(&cobaltapi.Deployment{
		ID: 1, Number: 1, Status: cobaltapi.StateQueued,
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"deploy", "--project", "api", "--no-follow", "--commit", "abc1234"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(string(api.lastBody), "abc1234") {
		t.Errorf("commit sha not in request body: %s", string(api.lastBody))
	}
}

func TestDeployWithNoCache(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond(&cobaltapi.Deployment{
		ID: 1, Number: 1, Status: cobaltapi.StateQueued,
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"deploy", "--project", "api", "--no-follow", "--no-cache"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(string(api.lastBody), `"noCache":true`) {
		t.Errorf("noCache not in request body: %s", string(api.lastBody))
	}
}
