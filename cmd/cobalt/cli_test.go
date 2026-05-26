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

func TestEmbeddedInitAssets(t *testing.T) {
	if !contains(initComposeTemplate, "${COBALT_IMAGE}") {
		t.Errorf("compose template missing COBALT_IMAGE substitution")
	}
	if !contains(initComposeTemplate, "${COBALT_PUBLIC_HOST}") {
		t.Errorf("compose template missing COBALT_PUBLIC_HOST substitution")
	}
	if !contains(initComposeTemplate, "${COBALT_DATA_DIR}") {
		t.Errorf("compose template missing COBALT_DATA_DIR substitution")
	}
	if !contains(initComposeTemplate, "rqlite/rqlite:") {
		t.Errorf("compose template missing rqlite image")
	}
	if !contains(initComposeTemplate, "image: caddy:") {
		t.Errorf("compose template missing caddy image")
	}
	// stack-deploy syntax: deploy.restart_policy.condition: any.
	if !contains(initComposeTemplate, "condition: any") {
		t.Errorf("compose template missing deploy.restart_policy")
	}
	// Encryption key delivered as a real swarm secret, not a host
	// bind mount any longer.
	if !contains(initComposeTemplate, "cobalt_encryption_key") {
		t.Errorf("compose template missing cobalt_encryption_key secret")
	}
	if !contains(initComposeTemplate, "external: true") {
		t.Errorf("compose template missing external: true (cobalt-main / secret declarations)")
	}
	for name, body := range map[string]string{
		"auto-https": initCaddyfileAutoHTTPS,
		"internal":   initCaddyfileInternal,
	} {
		if !contains(body, "reverse_proxy cobalt:8080") {
			t.Errorf("%s Caddyfile missing reverse_proxy to cobalt:8080", name)
		}
		if !contains(body, "admin unix//cobalt/caddy-socket/caddy.sock") {
			t.Errorf("%s Caddyfile missing admin unix socket", name)
		}
		// Both variants must name the :443 server "cobalt"; the daemon's
		// caddy admin client addresses /config/apps/http/servers/cobalt/.
		if !contains(body, "name cobalt") {
			t.Errorf("%s Caddyfile missing `servers :443 { name cobalt }`", name)
		}
	}
	if !contains(initCaddyfileAutoHTTPS, "{$COBALT_PUBLIC_HOST}") {
		t.Errorf("auto-https Caddyfile missing COBALT_PUBLIC_HOST placeholder")
	}
	if !contains(initCaddyfileInternal, "tls internal") {
		t.Errorf("internal Caddyfile missing tls internal directive")
	}
}

func TestCaddyfileFor(t *testing.T) {
	type tc struct {
		host        string
		insecureTLS bool
		want        string
	}
	cases := []tc{
		{"", false, "internal"},
		{"localhost", false, "internal"},
		{"127.0.0.1", false, "internal"},
		{"168.119.100.190", false, "internal"},
		{"::1", false, "internal"},
		{"cobalt.blue.cc", false, "auto-https"},
		{"e2e.example.com", false, "auto-https"},
		// --insecure-tls forces the internal variant even on a real domain.
		{"cobalt.blue.cc", true, "internal"},
	}
	for _, c := range cases {
		got := "auto-https"
		if caddyfileFor(c.host, c.insecureTLS) == initCaddyfileInternal {
			got = "internal"
		}
		if got != c.want {
			t.Errorf("caddyfileFor(%q, insecure=%v): got %s, want %s", c.host, c.insecureTLS, got, c.want)
		}
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
