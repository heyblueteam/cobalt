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
			"API_KEY": "k",
			"DB_URL":  "u",
			"AAA":     "a",
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

	// Every secret should appear with the env=KEY form so buildkit
	// resolves the value from the subprocess env (not as a filename).
	// Order is alphabetical so output is reproducible: AAA, API_KEY,
	// DB_URL. The aggregate `.env` secret follows the per-key block.
	wantSecrets := []string{
		"--secret", "id=AAA,env=AAA",
		"--secret", "id=API_KEY,env=API_KEY",
		"--secret", "id=DB_URL,env=DB_URL",
		"--secret", "id=.env,env=COBALT_DOT_ENV",
	}
	if !argSequence(args, wantSecrets...) {
		t.Errorf("secrets not in expected env=KEY form: %v", args)
	}

	// And the value of each must be on the subprocess env.
	env := r.lastCall().Env
	if env["AAA"] != "a" || env["API_KEY"] != "k" || env["DB_URL"] != "u" {
		t.Errorf("buildx subprocess env missing/wrong values: %v", env)
	}

	// COBALT_DOT_ENV holds the aggregate dotenv body, sorted keys, plain
	// form (no specials in these values).
	wantDotEnv := "AAA=a\nAPI_KEY=k\nDB_URL=u\n"
	if env["COBALT_DOT_ENV"] != wantDotEnv {
		t.Errorf("COBALT_DOT_ENV: got %q, want %q", env["COBALT_DOT_ENV"], wantDotEnv)
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
	if err := c.EnsureBuildxBuilder(context.Background(), BuildxBuilderName); err != nil {
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
	if err := c.EnsureBuildxBuilder(context.Background(), BuildxBuilderName); err != nil {
		t.Fatalf("EnsureBuildxBuilder: %v", err)
	}
	last := r.lastCall().Args
	if !argSequence(last, "buildx", "create", "--name", BuildxBuilderName, "--driver", "docker-container", "--bootstrap") {
		t.Errorf("expected buildx create with docker-container driver, got %v", last)
	}
}

// TestEnsureBuildxBuilder_CustomNameThreadsThrough proves the name
// argument reaches buildx unmodified — the per-project isolated builder
// path depends on this for the hybrid scheme (cobalt#24).
func TestEnsureBuildxBuilder_CustomNameThreadsThrough(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerErr("buildx inspect", staticErr("not found"))
	c := NewWithRunner(r)
	if err := c.EnsureBuildxBuilder(context.Background(), "cobalt-builder-42"); err != nil {
		t.Fatalf("EnsureBuildxBuilder: %v", err)
	}
	last := r.lastCall().Args
	if !argSequence(last, "buildx", "create", "--name", "cobalt-builder-42", "--driver", "docker-container", "--bootstrap") {
		t.Errorf("expected create with custom name, got %v", last)
	}
}

// TestRemoveBuildxBuilder_InspectsThenRemoves locks in the two-step
// shape: inspect to confirm the builder exists, then `buildx rm --force
// <name>` to tear it down. --force so removal never hangs waiting for
// an in-flight build (the dispatcher's per-project serialization
// guarantees no real concurrent build by the time cleanup runs).
func TestRemoveBuildxBuilder_InspectsThenRemoves(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	if err := c.RemoveBuildxBuilder(context.Background(), "cobalt-builder-42"); err != nil {
		t.Fatalf("RemoveBuildxBuilder: %v", err)
	}
	last := r.lastCall().Args
	if !argSequence(last, "buildx", "rm", "--force", "cobalt-builder-42") {
		t.Errorf("expected `buildx rm --force <name>`, got %v", last)
	}
}

// TestRemoveBuildxBuilder_MissingBuilderReturnsNil proves the
// inspect-first guard: when the builder doesn't exist (the common path
// for solo-project deletes), we skip `buildx rm` and return nil — the
// caller logs nothing instead of having to classify a non-zero rm exit
// as "expected missing" vs "real failure".
func TestRemoveBuildxBuilder_MissingBuilderReturnsNil(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerErr("buildx inspect", staticErr("no builder found"))
	c := NewWithRunner(r)
	if err := c.RemoveBuildxBuilder(context.Background(), "cobalt-builder-42"); err != nil {
		t.Fatalf("RemoveBuildxBuilder: %v", err)
	}
	for _, call := range r.calls {
		if len(call.Args) >= 2 && call.Args[0] == "buildx" && call.Args[1] == "rm" {
			t.Errorf("rm should not run when inspect fails; got %v", call.Args)
		}
	}
}

// TestBuild_BuilderNameThreadsThrough proves opts.BuilderName lands in
// argv as `--builder <name>`. This is the lever the deploy layer uses to
// route a build through an isolated per-project buildx (cobalt#24).
func TestBuild_BuilderNameThreadsThrough(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	if _, err := c.Build(context.Background(), BuildOpts{
		ProjectName:      "api",
		ImageName:        "default",
		DeploymentNumber: 1,
		BuilderName:      "cobalt-builder-42",
	}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	args := r.lastCall().Args
	if !argSequence(args, "--builder", "cobalt-builder-42") {
		t.Errorf("expected --builder cobalt-builder-42, got %v", args)
	}
}

// TestBuild_EmptyBuilderNameFallsBackToShared keeps every existing
// caller (and every solo project after the cobalt#24 change) on the
// shared builder by default.
func TestBuild_EmptyBuilderNameFallsBackToShared(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	if _, err := c.Build(context.Background(), BuildOpts{
		ProjectName: "api", ImageName: "default", DeploymentNumber: 1,
	}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	args := r.lastCall().Args
	if !argSequence(args, "--builder", BuildxBuilderName) {
		t.Errorf("expected fallback to shared builder, got %v", args)
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

// TestBuild_AggregateAlwaysEmitted: the `.env` aggregate secret must be
// present even when EnvSecrets is empty so Dockerfiles with an
// unconditional `RUN --mount=type=secret,id=.env ...` see a (possibly
// empty) file at /run/secrets/.env rather than a buildkit error.
func TestBuild_AggregateAlwaysEmitted(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	if _, err := c.Build(context.Background(), BuildOpts{
		ProjectName: "api", ImageName: "default", DeploymentNumber: 1,
	}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	args := r.lastCall().Args
	if !argSequence(args, "--secret", "id=.env,env=COBALT_DOT_ENV") {
		t.Errorf("aggregate secret missing for empty EnvSecrets: %v", args)
	}
	env := r.lastCall().Env
	if env["COBALT_DOT_ENV"] != "" {
		t.Errorf("COBALT_DOT_ENV: got %q, want empty", env["COBALT_DOT_ENV"])
	}
}

// TestBuild_DoesNotMutateCallerEnvSecrets: synthesizing COBALT_DOT_ENV
// must not appear back in the caller's input map. EnvVarMap returns
// state callers may reuse; a stray key would leak into per-key argv on
// the next build.
func TestBuild_DoesNotMutateCallerEnvSecrets(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	in := map[string]string{"FOO": "bar"}
	if _, err := c.Build(context.Background(), BuildOpts{
		ProjectName: "api", ImageName: "default", DeploymentNumber: 1,
		EnvSecrets: in,
	}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, ok := in["COBALT_DOT_ENV"]; ok {
		t.Errorf("caller's EnvSecrets was mutated: %v", in)
	}
	if len(in) != 1 {
		t.Errorf("caller's EnvSecrets size changed: %v", in)
	}
}

func TestFormatDotEnv(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   map[string]string
		want string
	}{
		{
			name: "empty",
			in:   nil,
			want: "",
		},
		{
			name: "plain",
			in:   map[string]string{"B": "two", "A": "one"},
			want: "A=one\nB=two\n",
		},
		{
			name: "value with newline",
			in:   map[string]string{"K": "line1\nline2"},
			want: "K=\"line1\\nline2\"\n",
		},
		{
			name: "value with carriage return",
			in:   map[string]string{"K": "a\rb"},
			want: "K=\"a\\rb\"\n",
		},
		{
			name: "value with double quote",
			in:   map[string]string{"K": `say "hi"`},
			want: "K=\"say \\\"hi\\\"\"\n",
		},
		{
			name: "value with backslash",
			in:   map[string]string{"K": `c:\path`},
			want: "K=\"c:\\\\path\"\n",
		},
		{
			name: "value with leading space",
			in:   map[string]string{"K": " leading"},
			want: "K=\" leading\"\n",
		},
		{
			name: "value with trailing space",
			in:   map[string]string{"K": "trailing "},
			want: "K=\"trailing \"\n",
		},
		{
			name: "value starting with hash",
			in:   map[string]string{"K": "#nope"},
			want: "K=\"#nope\"\n",
		},
		{
			name: "value with hash mid-string is plain",
			in:   map[string]string{"K": "abc#def"},
			want: "K=abc#def\n",
		},
		{
			name: "empty value",
			in:   map[string]string{"K": ""},
			want: "K=\n",
		},
		{
			name: "url with equals and slashes is plain",
			in:   map[string]string{"URL": "https://api.blue.cc/v1?x=1&y=2"},
			want: "URL=https://api.blue.cc/v1?x=1&y=2\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := formatDotEnv(tc.in)
			if got != tc.want {
				t.Errorf("formatDotEnv:\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}
