package deploy

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"sync"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

type fakeEnv struct {
	vars map[string]string
	err  error
}

func (f *fakeEnv) EnvVarMap(_ context.Context, _ int64) (map[string]string, error) {
	return f.vars, f.err
}

type fakeImageBuilder struct {
	mu    sync.Mutex
	calls []docker.BuildOpts
	tag   func(opts docker.BuildOpts) string
	err   error
}

func (f *fakeImageBuilder) Build(_ context.Context, opts docker.BuildOpts) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, opts)
	if f.err != nil {
		return "", f.err
	}
	if f.tag != nil {
		return f.tag(opts), nil
	}
	return docker.InternalImageName(opts.ProjectName, opts.ImageName, opts.DeploymentNumber), nil
}

func TestBuilder_BuildsEachUniqueImageOnce(t *testing.T) {
	t.Parallel()
	cf := &cobaltfile.Cobaltfile{
		Version: "1.0",
		Services: map[string]cobaltfile.Service{
			"web":    {Type: cobaltfile.TypeContainer, Image: "default", Port: 3000},
			"worker": {Type: cobaltfile.TypeContainer, Image: "default", Port: 8000},
			"docs":   {Type: cobaltfile.TypeContainer, Image: "alt", Port: 8000},
		},
		Images: map[string]cobaltfile.Image{
			"default": {Dockerfile: "Dockerfile", Context: "."},
			"alt":     {Dockerfile: "Dockerfile.alt", Context: "docs"},
		},
	}
	ws := &Workspace{Path: "/tmp/repo", Cobaltfile: cf, Commit: "abc"}
	d := &fakeImageBuilder{}
	b := NewBuilder(d, &fakeEnv{}, "/data")

	out, err := b.Build(context.Background(), store.Project{ID: 7, Name: "api"},
		store.Deployment{Number: 3}, ws, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(d.calls) != 2 {
		t.Errorf("build calls: got %d, want 2 (default + alt)", len(d.calls))
	}
	// Each service in cobaltfile gets a BuiltService.
	if len(out) != 3 {
		t.Errorf("services: got %d, want 3", len(out))
	}
	// Web and worker share the default image tag.
	tags := map[string]string{}
	for _, b := range out {
		tags[b.Name] = b.ImageTag
	}
	if tags["web"] != tags["worker"] {
		t.Errorf("web/worker should share image: web=%q worker=%q", tags["web"], tags["worker"])
	}
	if tags["docs"] == tags["web"] {
		t.Errorf("docs should have different image: docs=%q web=%q", tags["docs"], tags["web"])
	}
}

func TestBuilder_PassesCacheDir(t *testing.T) {
	t.Parallel()
	cf := &cobaltfile.Cobaltfile{
		Version:  "1.0",
		Services: map[string]cobaltfile.Service{"web": {Image: "default", Port: 3000}},
		Images:   map[string]cobaltfile.Image{"default": {Dockerfile: "Dockerfile", Context: "."}},
	}
	d := &fakeImageBuilder{}
	b := NewBuilder(d, &fakeEnv{}, "/data")

	_, err := b.Build(context.Background(),
		store.Project{ID: 42, Name: "api"},
		store.Deployment{Number: 5},
		&Workspace{Path: "/tmp/repo", Cobaltfile: cf},
		nil,
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(d.calls) != 1 {
		t.Fatalf("calls: %d", len(d.calls))
	}
	want := "/data/buildkit-cache/" + strconv.FormatInt(42, 10)
	if d.calls[0].CacheDir != want {
		t.Errorf("CacheDir: got %q, want %q", d.calls[0].CacheDir, want)
	}
}

func TestBuilder_StaticOnlySkipsBuild(t *testing.T) {
	t.Parallel()
	cf := &cobaltfile.Cobaltfile{
		Version: "1.0",
		Services: map[string]cobaltfile.Service{
			"web": {Type: cobaltfile.TypeStatic, PublicPath: "dist", Image: "default", Port: 8000},
		},
		Images: map[string]cobaltfile.Image{"default": {Dockerfile: "Dockerfile", Context: "."}},
	}
	d := &fakeImageBuilder{}
	b := NewBuilder(d, &fakeEnv{}, "")

	out, err := b.Build(context.Background(), store.Project{ID: 1, Name: "x"},
		store.Deployment{Number: 1}, &Workspace{Cobaltfile: cf}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(d.calls) != 0 {
		t.Errorf("static-only built: %d calls", len(d.calls))
	}
	if len(out) != 1 {
		t.Errorf("services: got %d, want 1", len(out))
	}
	if out[0].ImageTag != "" {
		t.Errorf("static service image tag: %q, want empty", out[0].ImageTag)
	}
}

func TestBuilder_PropagatesEnvSecrets(t *testing.T) {
	t.Parallel()
	cf := &cobaltfile.Cobaltfile{
		Version:  "1.0",
		Services: map[string]cobaltfile.Service{"web": {Image: "default", Port: 8000}},
		Images:   map[string]cobaltfile.Image{"default": {Dockerfile: "Dockerfile", Context: "."}},
	}
	d := &fakeImageBuilder{}
	b := NewBuilder(d, &fakeEnv{vars: map[string]string{"API_KEY": "k", "DB_URL": "u"}}, "")

	_, err := b.Build(context.Background(),
		store.Project{ID: 1, Name: "x"},
		store.Deployment{Number: 1, NoCache: true},
		&Workspace{Cobaltfile: cf},
		nil,
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(d.calls) != 1 {
		t.Fatalf("calls: %d", len(d.calls))
	}
	keys := make([]string, 0, len(d.calls[0].EnvSecrets))
	for k := range d.calls[0].EnvSecrets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"API_KEY", "DB_URL"}
	if len(keys) != 2 || keys[0] != want[0] || keys[1] != want[1] {
		t.Errorf("env secrets: %v", keys)
	}
	if !d.calls[0].NoCache {
		t.Error("NoCache not propagated")
	}
}

// TestBuilder_PrebuiltImageSkipsBuild proves that when `svc.Image` is
// NOT a key in `cf.Images`, the builder treats it as a pre-built docker
// registry reference: no docker build is invoked, and the resulting
// BuiltService.ImageTag is the verbatim image string. Matches disco's
// resolution rule in utils/docker.py:1218-1228.
//
// Real-world this covers projects like `redis` (uses
// `redis/redis-stack:7.4.0-v8`), `dozzle`, `grafana`, and the postgres
// /clickhouse /redis sub-services of `openpanel` and `plunk`.
func TestBuilder_PrebuiltImageSkipsBuild(t *testing.T) {
	t.Parallel()
	cf := &cobaltfile.Cobaltfile{
		Version: "1.0",
		Services: map[string]cobaltfile.Service{
			"redis": {
				Type:  cobaltfile.TypeContainer,
				Image: "redis/redis-stack:7.4.0-v8",
				Port:  6379,
			},
		},
		// no Images map entry — image is a pre-built ref
	}
	d := &fakeImageBuilder{}
	b := NewBuilder(d, &fakeEnv{}, "")
	out, err := b.Build(context.Background(),
		store.Project{ID: 1, Name: "redis"},
		store.Deployment{Number: 1},
		&Workspace{Cobaltfile: cf},
		nil,
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(d.calls) != 0 {
		t.Errorf("expected 0 docker builds for pre-built image, got %d", len(d.calls))
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 BuiltService, got %d", len(out))
	}
	if out[0].ImageTag != "redis/redis-stack:7.4.0-v8" {
		t.Errorf("ImageTag = %q, want verbatim pre-built ref %q", out[0].ImageTag, "redis/redis-stack:7.4.0-v8")
	}
}

// TestBuilder_MixedPrebuiltAndBuilt covers the multi-service shape used
// by openpanel and plunk: some services use pre-built images
// (postgres:14-alpine, redis:7.2.5-alpine), others use repo-built ones
// (clickhouse/Dockerfile). The builder must build only the
// repo-Dockerfile images and pass the pre-built refs through unchanged.
func TestBuilder_MixedPrebuiltAndBuilt(t *testing.T) {
	t.Parallel()
	cf := &cobaltfile.Cobaltfile{
		Version: "1.0",
		Services: map[string]cobaltfile.Service{
			"postgres":   {Type: cobaltfile.TypeContainer, Image: "postgres:14-alpine", Port: 5432},
			"redis":      {Type: cobaltfile.TypeContainer, Image: "redis:7.2.5-alpine", Port: 6379},
			"clickhouse": {Type: cobaltfile.TypeContainer, Image: "clickhouse", Port: 8123},
			"web":        {Type: cobaltfile.TypeContainer, Image: "web", Port: 80},
		},
		Images: map[string]cobaltfile.Image{
			"clickhouse": {Dockerfile: "clickhouse/Dockerfile", Context: "clickhouse"},
			"web":        {Dockerfile: "caddy/Dockerfile", Context: "caddy"},
		},
	}
	d := &fakeImageBuilder{}
	b := NewBuilder(d, &fakeEnv{}, "")
	out, err := b.Build(context.Background(),
		store.Project{ID: 1, Name: "openpanel"},
		store.Deployment{Number: 2},
		&Workspace{Cobaltfile: cf},
		nil,
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(d.calls) != 2 {
		t.Errorf("expected 2 docker builds (clickhouse + web), got %d", len(d.calls))
	}

	// Each BuiltService must carry the right ImageTag — pre-built refs
	// pass through verbatim; repo-built services get the cobalt-tagged
	// build output (whatever the fake builder returned).
	got := map[string]string{}
	for _, b := range out {
		got[b.Name] = b.ImageTag
	}
	wantPrebuilt := map[string]string{
		"postgres": "postgres:14-alpine",
		"redis":    "redis:7.2.5-alpine",
	}
	for name, want := range wantPrebuilt {
		if got[name] != want {
			t.Errorf("%s ImageTag = %q, want pre-built %q", name, got[name], want)
		}
	}
	for _, name := range []string{"clickhouse", "web"} {
		if got[name] == "" || got[name] == "clickhouse" || got[name] == "web" {
			t.Errorf("%s ImageTag = %q, want a cobalt-tagged built image (not the source name)", name, got[name])
		}
	}
}

func TestBuilder_PropagatesDockerError(t *testing.T) {
	t.Parallel()
	cf := &cobaltfile.Cobaltfile{
		Version:  "1.0",
		Services: map[string]cobaltfile.Service{"web": {Image: "default", Port: 8000}},
		Images:   map[string]cobaltfile.Image{"default": {Dockerfile: "Dockerfile", Context: "."}},
	}
	d := &fakeImageBuilder{err: errors.New("docker boom")}
	b := NewBuilder(d, &fakeEnv{}, "")
	_, err := b.Build(context.Background(),
		store.Project{ID: 1, Name: "x"},
		store.Deployment{Number: 1},
		&Workspace{Cobaltfile: cf},
		nil,
	)
	if err == nil {
		t.Error("expected error from docker")
	}
}
