package cliconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoad_MissingFile(t *testing.T) {
	t.Parallel()
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Servers == nil {
		t.Error("Servers map: nil, want empty map")
	}
}

func TestSaveAndLoad_Roundtrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := &Config{
		Servers: map[string]Server{
			"prod": {Host: "cobalt.blue.cc", APIKey: "k1", CurrentProject: "api"},
			"dev":  {Host: "dev.example.com", APIKey: "k2"},
		},
		DefaultServer: "prod",
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.DefaultServer != "prod" {
		t.Errorf("DefaultServer: got %q, want prod", got.DefaultServer)
	}
	if got.Servers["prod"].APIKey != "k1" {
		t.Errorf("prod APIKey: got %q, want k1", got.Servers["prod"].APIKey)
	}
	if got.Servers["prod"].CurrentProject != "api" {
		t.Errorf("prod CurrentProject: got %q, want api", got.Servers["prod"].CurrentProject)
	}
}

func TestSave_FilePerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix file perms")
	}
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, &Config{Servers: map[string]Server{}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	const want = 0o600
	if got := info.Mode().Perm(); got != want {
		t.Errorf("perm: got %o, want %o", got, want)
	}
}

func TestActive(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Servers: map[string]Server{
			"prod": {Host: "h1", APIKey: "k1"},
			"dev":  {Host: "h2", APIKey: "k2"},
		},
		DefaultServer: "prod",
	}

	t.Run("explicit picks named server", func(t *testing.T) {
		name, s, err := cfg.Active("dev")
		if err != nil {
			t.Fatalf("Active: %v", err)
		}
		if name != "dev" || s.Host != "h2" {
			t.Errorf("got %s/%s, want dev/h2", name, s.Host)
		}
	})

	t.Run("default falls back", func(t *testing.T) {
		name, s, err := cfg.Active("")
		if err != nil {
			t.Fatalf("Active: %v", err)
		}
		if name != "prod" || s.Host != "h1" {
			t.Errorf("got %s/%s, want prod/h1", name, s.Host)
		}
	})

	t.Run("unknown explicit errors", func(t *testing.T) {
		if _, _, err := cfg.Active("nope"); err == nil {
			t.Error("want error for unknown server")
		}
	})

	t.Run("no default and no explicit errors", func(t *testing.T) {
		c := &Config{Servers: map[string]Server{}}
		if _, _, err := c.Active(""); err == nil {
			t.Error("want error when nothing configured")
		}
	})
}

func TestSetCurrentProject(t *testing.T) {
	t.Parallel()
	cfg := &Config{Servers: map[string]Server{
		"prod": {Host: "h", APIKey: "k"},
	}}
	if err := cfg.SetCurrentProject("prod", "myapp"); err != nil {
		t.Fatalf("SetCurrentProject: %v", err)
	}
	if cfg.Servers["prod"].CurrentProject != "myapp" {
		t.Errorf("got %q, want myapp", cfg.Servers["prod"].CurrentProject)
	}
	if err := cfg.SetCurrentProject("nope", "x"); err == nil {
		t.Error("want error for unknown server")
	}
}
