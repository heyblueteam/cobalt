package docker

import (
	"context"
	"strings"
	"testing"
)

func TestBuild_DeterministicArgs(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)

	tag, err := c.Build(context.Background(), BuildOpts{
		ProjectID:        7,
		ProjectName:      "api",
		ImageName:        "default",
		DeploymentNumber: 3,
		Dockerfile:       "Dockerfile",
		Context:          "src",
		EnvSecrets: map[string]string{
			"API_KEY":  "k",
			"DB_URL":   "u",
			"AAA":      "a",
		},
		NoCache: true,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if tag != "cobalt/project-api-default:3" {
		t.Errorf("tag: got %q", tag)
	}
	args := r.lastCall().Args

	// argv must start with `buildx build --builder cobalt-builder --load -t <tag>`.
	wantPrefix := []string{"buildx", "build", "--builder", BuildxBuilderName, "--load", "-t", tag}
	for i, w := range wantPrefix {
		if i >= len(args) || args[i] != w {
			t.Errorf("argv[%d]: got %v, want %v", i, args[:min(len(wantPrefix), len(args))], wantPrefix)
			break
		}
	}

	// Every secret should appear; their order is alphabetical so output is
	// reproducible. AAA comes before API_KEY comes before DB_URL.
	wantSecrets := []string{
		"--secret", "id=AAA",
		"--secret", "id=API_KEY",
		"--secret", "id=DB_URL",
	}
	if !argSequence(args, wantSecrets...) {
		t.Errorf("secrets not in deterministic order: %v", args)
	}

	// Every label should be present.
	for _, l := range []string{
		"cobalt.project.id=7",
		"cobalt.project.name=api",
		"cobalt.service.name=default",
		"cobalt.deployment.number=3",
	} {
		if !argHas(args, l) {
			t.Errorf("missing label %q in %v", l, args)
		}
	}

	// --no-cache and -f Dockerfile must appear.
	if !argHas(args, "--no-cache", "-f", "Dockerfile") {
		t.Errorf("missing flags: %v", args)
	}

	// Build context comes last.
	if args[len(args)-1] != "src" {
		t.Errorf("context: got %q", args[len(args)-1])
	}
}

func TestBuild_RequiresName(t *testing.T) {
	t.Parallel()
	c := NewWithRunner(newFakeRunner())
	if _, err := c.Build(context.Background(), BuildOpts{}); err == nil {
		t.Error("Build with empty names: want error")
	}
}

func TestBuild_DefaultContextDot(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	if _, err := c.Build(context.Background(), BuildOpts{
		ProjectName: "api", ImageName: "default", DeploymentNumber: 1,
	}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	args := r.lastCall().Args
	if args[len(args)-1] != "." {
		t.Errorf("default context: got %q, want .", args[len(args)-1])
	}
}

func TestBuild_CacheDirAddsBothFlags(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	if _, err := c.Build(context.Background(), BuildOpts{
		ProjectName:      "api",
		ImageName:        "default",
		DeploymentNumber: 1,
		CacheDir:         "/cobalt/data/buildkit-cache/7",
	}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	args := r.lastCall().Args
	if !argSequence(args, "--cache-from", "type=local,src=/cobalt/data/buildkit-cache/7") {
		t.Errorf("--cache-from missing or wrong: %v", args)
	}
	if !argSequence(args, "--cache-to", "type=local,dest=/cobalt/data/buildkit-cache/7,mode=max") {
		t.Errorf("--cache-to missing or wrong: %v", args)
	}
}

func TestBuild_NoCacheDirOmitsFlags(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	_, _ = c.Build(context.Background(), BuildOpts{
		ProjectName: "api", ImageName: "default", DeploymentNumber: 1,
	})
	args := r.lastCall().Args
	for _, w := range []string{"--cache-from", "--cache-to"} {
		if argHas(args, w) {
			t.Errorf("unexpected %q in %v", w, args)
		}
	}
}

func TestEnsureBuildxBuilder_NoOpWhenInspectSucceeds(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	if err := c.EnsureBuildxBuilder(context.Background()); err != nil {
		t.Fatalf("EnsureBuildxBuilder: %v", err)
	}
	// Inspect succeeded (fake returns nil error by default), so create
	// must not have been called.
	if r.callCount() != 1 {
		t.Errorf("expected only inspect, got %d calls", r.callCount())
	}
	if r.lastCall().Args[1] != "inspect" {
		t.Errorf("expected `buildx inspect`, got %v", r.lastCall().Args)
	}
}

func TestEnsureBuildxBuilder_CreatesWhenInspectFails(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerErr("buildx inspect", staticErr("not found"))
	c := NewWithRunner(r)
	if err := c.EnsureBuildxBuilder(context.Background()); err != nil {
		t.Fatalf("EnsureBuildxBuilder: %v", err)
	}
	last := r.lastCall().Args
	if !argSequence(last, "buildx", "create", "--name", BuildxBuilderName, "--driver", "docker-container", "--bootstrap") {
		t.Errorf("expected buildx create with docker-container driver, got %v", last)
	}
}

func TestBuild_ErrorPropagates(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerErr("buildx build", staticErr("boom"))
	c := NewWithRunner(r)

	_, err := c.Build(context.Background(), BuildOpts{
		ProjectName: "api", ImageName: "default", DeploymentNumber: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("Build err: %v", err)
	}
}
