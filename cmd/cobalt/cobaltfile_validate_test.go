package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/heyblueteam/cobalt/internal/output"
)

const goodCobaltfile = `{
  "version": "1.0",
  "name": "fixture",
  "images": { "default": { "dockerfile": "Dockerfile", "context": "." } },
  "services": {
    "web": { "type": "container", "image": "default", "port": 3000 }
  }
}`

func TestValidate_OK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cobalt.json")
	if err := os.WriteFile(path, []byte(goodCobaltfile), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"validate", path})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !contains(buf.String(), "is valid (1 services, 1 images)") {
		t.Errorf("unexpected output: %q", buf.String())
	}
}

func TestValidate_RejectsBadVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cobalt.json")
	if err := os.WriteFile(path, []byte(`{"version":"9.9"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	root := newRootCmd()
	root.SetArgs([]string{"validate", path})
	root.SilenceErrors = true
	root.SilenceUsage = true
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error for unsupported version")
	}
	if !contains(err.Error(), "unsupported version") {
		t.Errorf("error %q does not name unsupported version", err)
	}
}

func TestValidate_MissingFile(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"validate", "/no/such/cobalt.json"})
	root.SilenceErrors = true
	root.SilenceUsage = true
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !contains(err.Error(), "no such file") {
		t.Errorf("error %q does not name missing file", err)
	}
}

func TestValidate_RejectsHookWithoutCommand(t *testing.T) {
	// Hook services have a name-dependent rule (must be type=command
	// with a non-empty command). The validator must enforce this so
	// pre-push hooks catch the mistake instead of waiting for the
	// deploy to fail mid-flight.
	const bad = `{
  "version": "1.0",
  "name": "x",
  "images": { "default": { "dockerfile": "Dockerfile", "context": "." } },
  "services": {
    "web": { "type": "container", "image": "default", "port": 3000 },
    "hook:deploy:start:before": { "type": "command", "image": "default" }
  }
}`
	dir := t.TempDir()
	path := filepath.Join(dir, "cobalt.json")
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	root := newRootCmd()
	root.SetArgs([]string{"validate", path})
	root.SilenceErrors = true
	root.SilenceUsage = true
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error for hook without command")
	}
	if !contains(err.Error(), "requires a command") {
		t.Errorf("error %q does not flag missing command", err)
	}
}
