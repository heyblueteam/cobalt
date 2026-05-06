package cliconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveProject_Precedence(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "cobalt.json"), `{"name":"from-file"}`)

	srv := Server{CurrentProject: "from-cliconfig"}

	t.Run("flag wins over everything", func(t *testing.T) {
		got, err := ResolveProject("from-flag", "from-env", dir, srv)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got != "from-flag" {
			t.Errorf("got %q, want from-flag", got)
		}
	})

	t.Run("env wins when no flag", func(t *testing.T) {
		got, err := ResolveProject("", "from-env", dir, srv)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got != "from-env" {
			t.Errorf("got %q, want from-env", got)
		}
	})

	t.Run("cobalt.json wins when no flag/env", func(t *testing.T) {
		got, err := ResolveProject("", "", dir, srv)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got != "from-file" {
			t.Errorf("got %q, want from-file", got)
		}
	})

	t.Run("cliconfig is the last fallback", func(t *testing.T) {
		got, err := ResolveProject("", "", t.TempDir(), srv)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got != "from-cliconfig" {
			t.Errorf("got %q, want from-cliconfig", got)
		}
	})
}

func TestResolveProject_AncestorWalk(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "cobalt.json"), `{"name":"in-root"}`)
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveProject("", "", deep, Server{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "in-root" {
		t.Errorf("got %q, want in-root", got)
	}
}

func TestResolveProject_NoSourcesErrors(t *testing.T) {
	t.Parallel()
	if _, err := ResolveProject("", "", t.TempDir(), Server{}); err == nil {
		t.Error("want error when nothing resolves")
	}
}

func TestResolveProject_IgnoresCobaltJSONWithoutName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "cobalt.json"), `{"services":{}}`)

	if _, err := ResolveProject("", "", dir, Server{}); err == nil {
		t.Error("want error when cobalt.json has no name field")
	}
}

func TestResolveProject_IgnoresInvalidJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "cobalt.json"), `not json`)

	if _, err := ResolveProject("", "", dir, Server{CurrentProject: "fallback"}); err != nil {
		t.Fatalf("expected fallthrough to cliconfig, got error: %v", err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
