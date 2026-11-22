package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRegistryHost(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"ghcr.io/heyblueteam/blue-server@sha256:x": "ghcr.io",
		"localhost:5000/server@sha256:x":           "localhost:5000",
		"localhost/server@sha256:x":                "localhost",
		"heyblueteam/server@sha256:x":              "docker.io",
		"redis@sha256:x":                           "docker.io",
	}
	for ref, want := range cases {
		if got := RegistryHost(ref); got != want {
			t.Errorf("RegistryHost(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestPullImage_AnonymousUsesDaemonConfig(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	if err := c.PullImage(context.Background(), "ghcr.io/a/b@sha256:x", nil, nil); err != nil {
		t.Fatal(err)
	}
	call := r.lastCall()
	if !argSequence(call.Args, "pull", "--quiet", "--", "ghcr.io/a/b@sha256:x") {
		t.Errorf("args: %v", call.Args)
	}
	if _, ok := call.Env["DOCKER_CONFIG"]; ok {
		t.Errorf("anonymous pull must not set DOCKER_CONFIG: %v", call.Env)
	}
}

func TestPullImage_AuthUsesTemporaryConfigAndRemovesIt(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	var dir string
	var cfg struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	r.onRun = func(_ []string, env map[string]string) {
		dir = env["DOCKER_CONFIG"]
		path := filepath.Join(dir, "config.json")
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("config missing during pull: %v", err)
			return
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("config mode: %v", info.Mode().Perm())
		}
		raw, _ := os.ReadFile(path)
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Errorf("config json: %v", err)
		}
	}
	c := NewWithRunner(r)
	auth := &RegistryAuth{Registry: "ghcr.io", Username: "bot", Password: "s3cret"}
	if err := c.PullImage(context.Background(), "ghcr.io/a/b@sha256:x", auth, nil); err != nil {
		t.Fatal(err)
	}
	if dir == "" {
		t.Fatal("DOCKER_CONFIG not set for authenticated pull")
	}
	want := base64.StdEncoding.EncodeToString([]byte("bot:s3cret"))
	if cfg.Auths["ghcr.io"].Auth != want {
		t.Errorf("auth entry: %+v", cfg.Auths)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("temporary config left behind: %v", err)
	}
	for _, a := range r.lastCall().Args {
		if a == "s3cret" || a == want {
			t.Errorf("credential leaked into argv: %v", r.lastCall().Args)
		}
	}
}

func TestPullImage_RemovesConfigOnFailure(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerErr("pull", errors.New("denied"))
	var dir string
	r.onRun = func(_ []string, env map[string]string) { dir = env["DOCKER_CONFIG"] }
	c := NewWithRunner(r)
	err := c.PullImage(context.Background(), "ghcr.io/a/b@sha256:x", &RegistryAuth{Registry: "ghcr.io", Username: "u", Password: "p"}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Errorf("temporary config left behind after failure: %v", statErr)
	}
}

func TestImageLabel(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerStdout("image inspect", `{"org.opencontainers.image.revision":"abc123"}`)
	c := NewWithRunner(r)
	got, err := c.ImageLabel(context.Background(), "img", "org.opencontainers.image.revision")
	if err != nil || got != "abc123" {
		t.Fatalf("got %q, %v", got, err)
	}
	r2 := newFakeRunner()
	r2.answerStdout("image inspect", `null`)
	got, err = NewWithRunner(r2).ImageLabel(context.Background(), "img", "x")
	if err != nil || got != "" {
		t.Fatalf("no labels: got %q, %v", got, err)
	}
}

func TestTagImage(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	if err := NewWithRunner(r).TagImage(context.Background(), "src@sha256:x", "cobalt/project-a-default:3"); err != nil {
		t.Fatal(err)
	}
	if !argSequence(r.lastCall().Args, "image", "tag", "--", "src@sha256:x", "cobalt/project-a-default:3") {
		t.Errorf("args: %v", r.lastCall().Args)
	}
}

func TestPullImage_DockerHubUsesIndexKey(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	var keys []string
	r.onRun = func(_ []string, env map[string]string) {
		raw, _ := os.ReadFile(filepath.Join(env["DOCKER_CONFIG"], "config.json"))
		var cfg struct {
			Auths map[string]any `json:"auths"`
		}
		_ = json.Unmarshal(raw, &cfg)
		for k := range cfg.Auths {
			keys = append(keys, k)
		}
	}
	auth := &RegistryAuth{Registry: RegistryHost("acme/private@sha256:x"), Username: "u", Password: "p"}
	if err := NewWithRunner(r).PullImage(context.Background(), "acme/private@sha256:x", auth, nil); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != "https://index.docker.io/v1/" {
		t.Fatalf("auth keys: %v", keys)
	}
}
