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

func TestBuilder_UnknownImageErrors(t *testing.T) {
	t.Parallel()
	cf := &cobaltfile.Cobaltfile{
		Version:  "1.0",
		Services: map[string]cobaltfile.Service{"web": {Image: "ghost", Port: 8000}},
		// images map missing "ghost"
	}
	b := NewBuilder(&fakeImageBuilder{}, &fakeEnv{}, "")
	_, err := b.Build(context.Background(),
		store.Project{Name: "x"},
		store.Deployment{Number: 1},
		&Workspace{Cobaltfile: cf},
		nil,
	)
	if err == nil {
		t.Error("expected error")
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
