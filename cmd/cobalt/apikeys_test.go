package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/heyblueteam/cobalt/internal/output"
)

func TestApikeysList(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond([]map[string]any{
		{"id": 1, "name": "prod-key", "createdAt": 1700000000},
		{"id": 2, "name": "dev-key", "createdAt": 1700000001},
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"apikeys", "list"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(buf.String(), "prod-key") || !contains(buf.String(), "dev-key") {
		t.Errorf("missing keys: %s", buf.String())
	}
}

func TestApikeysListJSON(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond([]map[string]any{{"id": 1, "name": "prod-key", "createdAt": 1700000000}})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"apikeys", "list", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("expected JSON output")
	}
}

func TestApikeysCreate(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond(map[string]any{
		"id": 1, "name": "new-key", "key": "sk-secret", "createdAt": 1700000000,
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"apikeys", "create", "new-key"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(buf.String(), "sk-secret") {
		t.Errorf("expected raw key: %s", buf.String())
	}
}

func TestApikeysCreateJSON(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond(map[string]any{
		"id": 1, "name": "new-key", "key": "sk-secret", "createdAt": 1700000000,
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"apikeys", "create", "new-key", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("expected JSON output")
	}
}

func TestApikeysRemove(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond(nil)

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"apikeys", "remove", "1", "--yes"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if api.lastMethod != "DELETE" {
		t.Errorf("method: got %q, want DELETE", api.lastMethod)
	}
}
