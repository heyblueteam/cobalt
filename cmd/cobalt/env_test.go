package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func TestEnvList(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond([]cobaltapi.EnvVar{
		{Key: "FOO", Value: "bar"},
		{Key: "NODE_ENV", Value: "production"},
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"env", "list", "--project", "api"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := buf.String()
	if !contains(got, "FOO") || !contains(got, "bar") {
		t.Errorf("missing FOO=bar: %s", got)
	}
}

func TestEnvListJSON(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond([]cobaltapi.EnvVar{{Key: "FOO", Value: "bar"}})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"env", "list", "--project", "api", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("expected JSON output")
	}
}

func TestEnvListNoVars(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond([]cobaltapi.EnvVar{})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"env", "list", "--project", "api"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(buf.String(), "No env vars") {
		t.Errorf("expected empty message: %s", buf.String())
	}
}

func TestEnvSet(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond([]cobaltapi.EnvVar{
		{Key: "FOO", Value: "bar"},
		{Key: "NODE_ENV", Value: "production"},
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"env", "set", "FOO=bar", "NODE_ENV=production", "--project", "api"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
}

func TestEnvSetWithRedeploy(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond([]cobaltapi.EnvVar{{Key: "FOO", Value: "bar"}})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"env", "set", "FOO=bar", "--project", "api", "--redeploy"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
}

func TestEnvSetInvalidFormat(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"env", "set", "INVALID", "--project", "api"})
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error for missing = in argument")
	}
	if !contains(err.Error(), "invalid format") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEnvSetNoArgs(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"env", "set", "--project", "api"})
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error for no args")
	}
}

func TestEnvRemove(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond(nil)

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"env", "remove", "FOO", "--project", "api", "--yes"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
}
