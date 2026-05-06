package caddy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteInitConfig_Standalone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "init", "config.json")
	if err := WriteInitConfig(path, "cobalt.example.com", false); err != nil {
		t.Fatalf("WriteInitConfig: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// Admin must be on the unix socket.
	admin := cfg["admin"].(map[string]any)
	if got := admin["listen"]; got != "unix//cobalt/caddy-socket/caddy.sock" {
		t.Errorf("admin.listen: got %v", got)
	}

	// Server must listen on :443 in standalone mode.
	srv := cfg["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)["cobalt"].(map[string]any)
	listens := srv["listen"].([]any)
	if len(listens) != 1 || listens[0] != ":443" {
		t.Errorf("listen: got %v, want [:443]", listens)
	}

	// The daemon-host route should match cobalt.example.com.
	routes := srv["routes"].([]any)
	if len(routes) != 1 {
		t.Fatalf("routes len: %d", len(routes))
	}
	route := routes[0].(map[string]any)
	if route["@id"] != "cobalt-daemon-host" {
		t.Errorf("@id: got %v", route["@id"])
	}
	matchHosts := route["match"].([]any)[0].(map[string]any)["host"].([]any)
	if matchHosts[0] != "cobalt.example.com" {
		t.Errorf("matched host: got %v", matchHosts[0])
	}
}

func TestWriteInitConfig_BehindTunnel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := WriteInitConfig(path, "h.example.com", true); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var cfg map[string]any
	_ = json.Unmarshal(b, &cfg)

	srv := cfg["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)["cobalt"].(map[string]any)
	listens := srv["listen"].([]any)
	if listens[0] != ":80" {
		t.Errorf("behind-tunnel listen: got %v, want [:80]", listens)
	}
}

func TestWriteInitConfig_FilePerms(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := WriteInitConfig(path, "h", false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Errorf("perm: got %o", got)
	}
}

func TestIDHelpers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		got, want string
	}{
		{ProjectRouteID(7), "cobalt-project-7"},
		{ProjectHostsID(7), "cobalt-project-hosts-7"},
		{ProjectHandlerID(7), "cobalt-project-handler-7"},
		{RedirectID(99), "cobalt-redirect-99"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}
