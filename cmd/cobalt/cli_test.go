package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/heyblueteam/cobalt/internal/cliconfig"
	"github.com/heyblueteam/cobalt/internal/output"
)

func configAt(t *testing.T, tmpDir string, cfg *cliconfig.Config) string {
	t.Helper()
	cobaltDir := filepath.Join(tmpDir, ".cobalt")
	if err := os.MkdirAll(cobaltDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(cobaltDir, "config.json")
	if err := cliconfig.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func TestServersList(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	configAt(t, tmpDir, &cliconfig.Config{
		Servers: map[string]cliconfig.Server{
			"prod": {Host: "cobalt.blue.cc", APIKey: "k1", CurrentProject: "api"},
			"dev":  {Host: "dev.example.com", APIKey: "k2"},
		},
		DefaultServer: "prod",
	})

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"servers", "list"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := buf.String()
	if !contains(got, "prod") || !contains(got, "cobalt.blue.cc") {
		t.Errorf("missing prod: %s", got)
	}
	if !contains(got, "dev") || !contains(got, "dev.example.com") {
		t.Errorf("missing dev: %s", got)
	}
}

func TestServersListJSON(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	configAt(t, tmpDir, &cliconfig.Config{
		Servers:       map[string]cliconfig.Server{"prod": {Host: "h", APIKey: "k"}},
		DefaultServer: "prod",
	})

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"servers", "list", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("expected JSON output")
	}
}

func TestServersRemove(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	configAt(t, tmpDir, &cliconfig.Config{
		Servers:       map[string]cliconfig.Server{"old": {Host: "h", APIKey: "k"}},
		DefaultServer: "old",
	})

	root := newRootCmd()
	root.SetArgs([]string{"servers", "remove", "old", "--yes"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}

	cpath, err := cliconfig.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := cliconfig.Load(cpath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.Servers["old"]; ok {
		t.Error("server was not removed")
	}
	if loaded.DefaultServer != "" {
		t.Errorf("default server not cleared: %q", loaded.DefaultServer)
	}
}

func TestServersRemoveUnknown(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	configAt(t, tmpDir, &cliconfig.Config{Servers: map[string]cliconfig.Server{}})

	var buf bytes.Buffer
	oldStderr := output.Stderr
	output.Stderr = &buf
	defer func() { output.Stderr = oldStderr }()

	root := newRootCmd()
	root.SetArgs([]string{"servers", "remove", "nope", "--yes"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(buf.String(), "not found") {
		t.Errorf("expected 'not found' in stderr: %s", buf.String())
	}
}

func TestUse(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	configAt(t, tmpDir, &cliconfig.Config{
		Servers:       map[string]cliconfig.Server{"prod": {Host: "h", APIKey: "k"}},
		DefaultServer: "prod",
	})

	root := newRootCmd()
	root.SetArgs([]string{"use", "myapp"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}

	cpath, err := cliconfig.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := cliconfig.Load(cpath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Servers["prod"].CurrentProject != "myapp" {
		t.Errorf("got %q, want myapp", loaded.Servers["prod"].CurrentProject)
	}
}

func TestUseExplicitServer(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	configAt(t, tmpDir, &cliconfig.Config{
		Servers: map[string]cliconfig.Server{
			"prod": {Host: "h", APIKey: "k"},
			"dev":  {Host: "h2", APIKey: "k2"},
		},
		DefaultServer: "prod",
	})

	root := newRootCmd()
	root.SetArgs([]string{"use", "myapp", "--server", "dev"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}

	cpath, err := cliconfig.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := cliconfig.Load(cpath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Servers["prod"].CurrentProject != "" {
		t.Errorf("prod should not change, got %q", loaded.Servers["prod"].CurrentProject)
	}
	if loaded.Servers["dev"].CurrentProject != "myapp" {
		t.Errorf("dev: got %q, want myapp", loaded.Servers["dev"].CurrentProject)
	}
}

func TestVersion(t *testing.T) {
	root := newRootCmd()
	if root.Version != "dev" {
		t.Errorf("version: got %q, want dev", root.Version)
	}
}

func TestSubcommandsRegistered(t *testing.T) {
	root := newRootCmd()
	for _, name := range []string{"server", "servers", "use", "init"} {
		cmd, _, err := root.Find([]string{name})
		if err != nil {
			t.Errorf("subcommand %q not found: %v", name, err)
			continue
		}
		if cmd.Name() != name {
			t.Errorf("name: got %s, want %s", cmd.Name(), name)
		}
	}
}

func TestInitCommand_Help(t *testing.T) {
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"init", "--help"})
	err := root.Execute()
	if err != nil {
		t.Fatalf("execute --help: %v", err)
	}
	got := buf.String()
	if !contains(got, "SSH into a target host") {
		t.Errorf("missing description in help: %s", got)
	}
	if !contains(got, "--version") {
		t.Errorf("missing --version flag in help: %s", got)
	}
}

func TestInitCommand_Args(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"init"})
	err := root.Execute()
	if err == nil {
		t.Error("expected error for missing args, got nil")
	}
}

func TestDefaultComposeYAML(t *testing.T) {
	yaml, err := defaultComposeYAML("v1.0.0", "cobalt.example.com", "/custom/data")
	if err != nil {
		t.Fatalf("defaultComposeYAML: %v", err)
	}

	if !contains(yaml, "ghcr.io/heyblueteam/cobalt:v1.0.0") {
		t.Errorf("compose yaml missing expected image tag")
	}
	if !contains(yaml, "cobalt.example.com") {
		t.Errorf("compose yaml missing public host")
	}
	if !contains(yaml, "/custom/data") {
		t.Errorf("compose yaml missing custom data dir")
	}
	if !contains(yaml, "rqlite") {
		t.Errorf("compose yaml missing rqlite service")
	}
	if !contains(yaml, "caddy") {
		t.Errorf("compose yaml missing caddy service")
	}
	if !contains(yaml, "unless-stopped") {
		t.Errorf("compose yaml missing restart policy")
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}