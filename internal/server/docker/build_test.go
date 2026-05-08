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

	// argv must start with `build -t <tag>`.
	if args[0] != "build" || args[1] != "-t" || args[2] != tag {
		t.Errorf("prefix: %v", args[:3])
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

// TestBuild_CacheDirIsNoOp asserts the buildx-only cache flags stay out
// of argv until the daemon image installs the buildx plugin. CacheDir is
// preserved on BuildOpts for forward compatibility.
func TestBuild_CacheDirIsNoOp(t *testing.T) {
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
	for _, w := range []string{"--cache-from", "--cache-to"} {
		if argHas(args, w) {
			t.Errorf("buildx-only %q should not appear with classic builder: %v", w, args)
		}
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

func TestBuild_ErrorPropagates(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerErr("build", staticErr("boom"))
	c := NewWithRunner(r)

	_, err := c.Build(context.Background(), BuildOpts{
		ProjectName: "api", ImageName: "default", DeploymentNumber: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("Build err: %v", err)
	}
}
