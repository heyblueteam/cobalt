package deploy

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi/validator"
)

const testRef = "ghcr.io/heyblueteam/blue-server@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

type fakePuller struct {
	mu       sync.Mutex
	pulls    []string
	auths    []*docker.RegistryAuth
	tags     [][2]string
	removed  []string
	revision string
	pullErr  error
}

func (f *fakePuller) PullImage(_ context.Context, ref string, auth *docker.RegistryAuth, _ io.Writer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulls = append(f.pulls, ref)
	f.auths = append(f.auths, auth)
	return f.pullErr
}

func (f *fakePuller) TagImage(_ context.Context, src, dst string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tags = append(f.tags, [2]string{src, dst})
	return nil
}

func (f *fakePuller) ImageLabel(_ context.Context, _, key string) (string, error) {
	if key != RevisionLabel {
		return "", nil
	}
	return f.revision, nil
}

func (f *fakePuller) RemoveImage(_ context.Context, tag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, tag)
	return nil
}

type fakeRegistries map[string]store.RegistryCredential

func (f fakeRegistries) GetRegistryCredential(_ context.Context, host string) (*store.RegistryCredential, error) {
	c, ok := f[host]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &c, nil
}

// serverCobaltfile mirrors server/cobalt.json: two services, one image.
func serverCobaltfile() *cobaltfile.Cobaltfile {
	return &cobaltfile.Cobaltfile{
		Version: "1.0",
		Services: map[string]cobaltfile.Service{
			"api":    {Type: cobaltfile.TypeContainer, Image: "default", Port: 4000, ExposedInternally: true},
			"worker": {Type: cobaltfile.TypeContainer, Image: "default", Command: "blue worker"},
			"redis":  {Type: cobaltfile.TypeContainer, Image: "redis:7"},
		},
		Images: map[string]cobaltfile.Image{"default": {Dockerfile: "Dockerfile", Context: "."}},
	}
}

func TestBuilder_PrebuiltPullsTagsAndNeverBuilds(t *testing.T) {
	t.Parallel()
	d := &fakeImageBuilder{}
	p := &fakePuller{revision: "abc1234"}
	regs := fakeRegistries{"ghcr.io": {Username: "bot", Password: "tok"}}
	b := NewBuilderWithPull(d, &fakeEnv{}, &fakeProjectQuerier{}, "/data", p, regs)

	out, err := b.Build(context.Background(),
		store.Project{ID: 1, Name: "server", ImageSource: validator.ImageSourceRegistry},
		store.Deployment{Number: 12, ImageRefs: map[string]string{"default": testRef}},
		&Workspace{Path: "/tmp/repo", Cobaltfile: serverCobaltfile(), Commit: "abc1234"}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(d.calls) != 0 {
		t.Fatalf("prebuilt deploy ran %d builds", len(d.calls))
	}
	if len(p.pulls) != 1 || p.pulls[0] != testRef {
		t.Fatalf("pulls: %v", p.pulls)
	}
	if p.auths[0] == nil || p.auths[0].Registry != "ghcr.io" || p.auths[0].Username != "bot" || p.auths[0].Password != "tok" {
		t.Fatalf("auth: %+v", p.auths[0])
	}
	wantTag := "cobalt/project-server-default:12"
	if len(p.tags) != 1 || p.tags[0] != [2]string{testRef, wantTag} {
		t.Fatalf("tags: %v", p.tags)
	}
	if len(p.removed) != 1 || p.removed[0] != testRef {
		t.Fatalf("pulled name not dropped: %v", p.removed)
	}
	tags := map[string]string{}
	for _, s := range out {
		tags[s.Name] = s.ImageTag
	}
	if tags["api"] != wantTag || tags["worker"] != wantTag {
		t.Errorf("service tags: %v", tags)
	}
	if tags["redis"] != "redis:7" {
		t.Errorf("registry service from cobalt.json must pass through: %v", tags)
	}
}

func TestBuilder_PrebuiltAnonymousWithoutLogin(t *testing.T) {
	t.Parallel()
	p := &fakePuller{}
	b := NewBuilderWithPull(&fakeImageBuilder{}, &fakeEnv{}, &fakeProjectQuerier{}, "", p, fakeRegistries{})
	_, err := b.Build(context.Background(), store.Project{Name: "server"},
		store.Deployment{Number: 1, ImageRefs: map[string]string{"default": testRef}},
		&Workspace{Cobaltfile: serverCobaltfile()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.auths[0] != nil {
		t.Errorf("expected anonymous pull, got %+v", p.auths[0])
	}
}

func TestBuilder_PrebuiltRefusals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		project  store.Project
		refs     map[string]string
		revision string
		pullErr  error
		want     string
	}{
		{"registry project without refs", store.Project{Name: "server", ImageSource: validator.ImageSourceRegistry}, nil, "", nil, "deploys prebuilt images"},
		{"unknown image name", store.Project{Name: "server"}, map[string]string{"default": testRef, "docs": testRef}, "", nil, "not used by any service"},
		{"missing image", store.Project{Name: "server"}, map[string]string{"other": testRef}, "", nil, "not used by any service"},
		{"revision mismatch", store.Project{Name: "server"}, map[string]string{"default": testRef}, "fff0000", nil, "built from fff0000"},
		{"pull failure", store.Project{Name: "server"}, map[string]string{"default": testRef}, "", errors.New("denied"), "denied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := &fakeImageBuilder{}
			p := &fakePuller{revision: tc.revision, pullErr: tc.pullErr}
			b := NewBuilderWithPull(d, &fakeEnv{}, &fakeProjectQuerier{}, "", p, fakeRegistries{})
			_, err := b.Build(context.Background(), tc.project,
				store.Deployment{Number: 2, ImageRefs: tc.refs},
				&Workspace{Cobaltfile: serverCobaltfile(), Commit: "abc1234"}, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if len(d.calls) != 0 {
				t.Errorf("refused prebuilt deploy fell back to %d builds", len(d.calls))
			}
			if len(p.tags) != 0 {
				t.Errorf("refused deploy tagged images: %v", p.tags)
			}
		})
	}
}

func TestBuilder_PrebuiltNeedsEveryImage(t *testing.T) {
	t.Parallel()
	cf := serverCobaltfile()
	cf.Images["docs"] = cobaltfile.Image{Dockerfile: "Dockerfile.docs", Context: "docs"}
	cf.Services["docs"] = cobaltfile.Service{Type: cobaltfile.TypeContainer, Image: "docs", Port: 80}
	d := &fakeImageBuilder{}
	b := NewBuilderWithPull(d, &fakeEnv{}, &fakeProjectQuerier{}, "", &fakePuller{}, fakeRegistries{})
	_, err := b.Build(context.Background(), store.Project{Name: "server"},
		store.Deployment{Number: 2, ImageRefs: map[string]string{"default": testRef}},
		&Workspace{Cobaltfile: cf}, nil)
	if err == nil || !strings.Contains(err.Error(), `no prebuilt reference for image "docs"`) {
		t.Fatalf("err = %v", err)
	}
	if len(d.calls) != 0 {
		t.Errorf("mixed build/pull is not allowed; got %d builds", len(d.calls))
	}
}

func TestBuilder_BuildProjectsIgnorePuller(t *testing.T) {
	t.Parallel()
	d := &fakeImageBuilder{}
	p := &fakePuller{}
	b := NewBuilderWithPull(d, &fakeEnv{}, &fakeProjectQuerier{}, "", p, fakeRegistries{})
	_, err := b.Build(context.Background(), store.Project{Name: "api", ImageSource: validator.ImageSourceBuild},
		store.Deployment{Number: 2}, &Workspace{Cobaltfile: serverCobaltfile()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.calls) != 1 || len(p.pulls) != 0 {
		t.Errorf("builds=%d pulls=%d, want 1 build and no pull", len(d.calls), len(p.pulls))
	}
}

func TestBuilder_PrebuiltWarnsWithoutRevisionLabel(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	b := NewBuilderWithPull(&fakeImageBuilder{}, &fakeEnv{}, &fakeProjectQuerier{}, "", &fakePuller{}, fakeRegistries{})
	_, err := b.Build(context.Background(), store.Project{Name: "server"},
		store.Deployment{Number: 1, ImageRefs: map[string]string{"default": testRef}},
		&Workspace{Cobaltfile: serverCobaltfile(), Commit: "abc1234"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "has no "+RevisionLabel+" label") {
		t.Fatalf("missing-label warning not logged:\n%s", out.String())
	}
}
