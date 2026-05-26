package docker

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCreateService_FullArgs(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)

	err := c.CreateService(context.Background(), ServiceCreateOpts{
		ProjectID:        7,
		ProjectName:      "api",
		ServiceName:      "web",
		DeploymentNumber: 3,
		Image:            "cobalt/project-api-default:3",
		Command:          "node server.js",
		EnvVars:          map[string]string{"PORT": "3000", "DEBUG": "1"},
		PublishedPorts: []PublishedPort{
			{PublishedAs: 80, FromContainerPort: 3000, Protocol: "tcp"},
		},
		Networks:          []NetworkAttachment{{Name: "cobalt-project-api-3"}},
		Replicas:          2,
		HealthCommand:     "curl -f http://localhost:3000/healthz",
		HealthStartPeriod: 10 * time.Minute,
		HealthInterval:    5 * time.Second,
		Volumes: []ServiceVolume{
			{VolumeName: "cobalt-volume-7-data", DestinationPath: "/data"},
		},
		ExtraParams: SplitParams("--host host.docker.internal:host-gateway"),
	})
	if err != nil {
		t.Fatalf("CreateService: %v", err)
	}
	args := r.lastCall().Args

	for _, want := range []string{
		"service", "create",
		"--name", "api-3-web",
		"--with-registry-auth",
	} {
		if !argHas(args, want) {
			t.Errorf("missing %q in %v", want, args)
		}
	}

	if !argSequence(args, "--name", "api-3-web") {
		t.Errorf("--name pair missing")
	}
	if !argSequence(args, "--replicas", "2") {
		t.Errorf("--replicas pair missing")
	}
	if !argSequence(args, "--health-cmd", "curl -f http://localhost:3000/healthz") {
		t.Errorf("--health-cmd pair missing")
	}
	if !argSequence(args, "--health-start-period", "10m0s") {
		t.Errorf("--health-start-period: %v", args)
	}
	if !argSequence(args, "--network", "cobalt-project-api-3") {
		t.Errorf("--network pair missing")
	}
	if !argSequence(args, "--publish", "published=80,target=3000,protocol=tcp") {
		t.Errorf("--publish: %v", args)
	}
	if !argSequence(args, "--mount", "type=volume,source=cobalt-volume-7-data,destination=/data") {
		t.Errorf("--mount: %v", args)
	}

	// Image and command are positional and come after all flags.
	// Command is shell-split, so "node server.js" becomes ["node", "server.js"]:
	// image at -3, command tokens at -2 and -1.
	if args[len(args)-3] != "cobalt/project-api-default:3" {
		t.Errorf("image position wrong: %v", args)
	}
	if args[len(args)-2] != "node" || args[len(args)-1] != "server.js" {
		t.Errorf("command tokens wrong: got [%s %s], want [node server.js]", args[len(args)-2], args[len(args)-1])
	}

	// Env vars must be present in alphabetical key order.
	if !argSequence(args, "--env", "DEBUG=1") {
		t.Errorf("env DEBUG: %v", args)
	}
	if !argSequence(args, "--env", "PORT=3000") {
		t.Errorf("env PORT: %v", args)
	}

	// Extra params should appear before the image (they're --host etc.).
	if !argSequence(args, "--host", "host.docker.internal:host-gateway") {
		t.Errorf("extra params missing: %v", args)
	}
}

func TestCreateService_DefaultsForHealth(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	_ = c.CreateService(context.Background(), ServiceCreateOpts{
		ProjectName: "api", ServiceName: "web", DeploymentNumber: 1,
		Image:         "img:1",
		HealthCommand: "true",
	})
	args := r.lastCall().Args
	if !argSequence(args, "--health-start-period", "5m0s") {
		t.Errorf("default --health-start-period: %v", args)
	}
	if !argSequence(args, "--health-start-interval", "3s") {
		t.Errorf("default --health-start-interval: %v", args)
	}
}

func TestCreateService_NoHealthFlag(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	_ = c.CreateService(context.Background(), ServiceCreateOpts{
		ProjectName: "api", ServiceName: "web", DeploymentNumber: 1,
		Image: "img:1",
	})
	args := r.lastCall().Args
	for _, w := range []string{"--health-cmd", "--health-start-period", "--health-start-interval"} {
		if argHas(args, w) {
			t.Errorf("unexpected %q in %v", w, args)
		}
	}
}

// TestCreateService_NetworkWithAlias verifies that a NetworkAttachment
// with a non-empty Alias renders as `--network name=<net>,alias=<alias>`,
// the docker syntax that registers a per-network DNS alias for the
// service. This is the load-bearing case: cross-project env vars like
// `REDIS_HOST=redis-redis` depend on swarm DNS resolving the alias.
func TestCreateService_NetworkWithAlias(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	err := c.CreateService(context.Background(), ServiceCreateOpts{
		ProjectName: "redis", ServiceName: "redis", DeploymentNumber: 1,
		Image: "redis/redis-stack:7.4.0-v8",
		Networks: []NetworkAttachment{
			{Name: "cobalt-project-redis-1", Alias: "redis"},
			{Name: "cobalt-main", Alias: "redis-redis"},
		},
	})
	if err != nil {
		t.Fatalf("CreateService: %v", err)
	}
	args := r.lastCall().Args

	if !argSequence(args, "--network", "name=cobalt-project-redis-1,alias=redis") {
		t.Errorf("per-deploy alias missing: %v", args)
	}
	if !argSequence(args, "--network", "name=cobalt-main,alias=redis-redis") {
		t.Errorf("cobalt-main alias missing: %v", args)
	}
}

// TestCreateService_NetworkWithoutAlias verifies that a NetworkAttachment
// with an empty Alias renders as the bare `--network <net>` form, matching
// the pre-alias behavior for any caller that doesn't need DNS aliasing.
func TestCreateService_NetworkWithoutAlias(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	err := c.CreateService(context.Background(), ServiceCreateOpts{
		ProjectName: "api", ServiceName: "web", DeploymentNumber: 3,
		Image: "img:1",
		Networks: []NetworkAttachment{
			{Name: "cobalt-project-api-3"}, // no alias
		},
	})
	if err != nil {
		t.Fatalf("CreateService: %v", err)
	}
	args := r.lastCall().Args

	if !argSequence(args, "--network", "cobalt-project-api-3") {
		t.Errorf("bare network missing: %v", args)
	}
	// Defensive: the comma-form must NOT appear when alias is empty.
	for _, a := range args {
		if a == "name=cobalt-project-api-3,alias=" {
			t.Errorf("empty alias rendered with trailing comma: %v", args)
		}
	}
}

// TestCreateService_NetworkMixed proves the renderer makes per-attachment
// decisions (bare vs name+alias) independently when both forms coexist in
// the same Networks slice. Without this, a caller with one network needing
// an alias and another not might silently render both the same way.
func TestCreateService_NetworkMixed(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	err := c.CreateService(context.Background(), ServiceCreateOpts{
		ProjectName: "api", ServiceName: "web", DeploymentNumber: 3,
		Image: "img:1",
		Networks: []NetworkAttachment{
			{Name: "cobalt-project-api-3", Alias: "web"},
			{Name: "cobalt-main"}, // no alias
		},
	})
	if err != nil {
		t.Fatalf("CreateService: %v", err)
	}
	args := r.lastCall().Args

	if !argSequence(args, "--network", "name=cobalt-project-api-3,alias=web") {
		t.Errorf("aliased entry: %v", args)
	}
	if !argSequence(args, "--network", "cobalt-main") {
		t.Errorf("bare entry: %v", args)
	}
}

// TestCreateService_NetworkOrderPreserved asserts the rendered --network
// flags appear in the same order callers provided. Network order can
// matter for downstream tooling (first-network primary), and silent
// reordering would be a footgun.
func TestCreateService_NetworkOrderPreserved(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	err := c.CreateService(context.Background(), ServiceCreateOpts{
		ProjectName: "api", ServiceName: "web", DeploymentNumber: 1,
		Image: "img:1",
		Networks: []NetworkAttachment{
			{Name: "first", Alias: "alpha"},
			{Name: "second", Alias: "beta"},
			{Name: "third"}, // bare
		},
	})
	if err != nil {
		t.Fatalf("CreateService: %v", err)
	}
	args := r.lastCall().Args

	// Find each --network's argument index and check ordering.
	var idx []int
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--network" {
			idx = append(idx, i+1)
		}
	}
	if len(idx) != 3 {
		t.Fatalf("expected 3 --network args, got %d: %v", len(idx), args)
	}
	if args[idx[0]] != "name=first,alias=alpha" {
		t.Errorf("[0] %s", args[idx[0]])
	}
	if args[idx[1]] != "name=second,alias=beta" {
		t.Errorf("[1] %s", args[idx[1]])
	}
	if args[idx[2]] != "third" {
		t.Errorf("[2] %s", args[idx[2]])
	}
}

// TestCreateService_NetworkFlagValue exercises the pure renderer directly
// so an alias bug (e.g. forgetting the `name=` prefix, or omitting the
// alias entirely) shows up here even if the higher-level test happens to
// pass for unrelated reasons.
func TestNetworkFlagValue(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   NetworkAttachment
		want string
	}{
		{NetworkAttachment{Name: "n"}, "n"},
		{NetworkAttachment{Name: "n", Alias: ""}, "n"},
		{NetworkAttachment{Name: "cobalt-main", Alias: "redis-redis"}, "name=cobalt-main,alias=redis-redis"},
		{NetworkAttachment{Name: "n", Alias: "with-hyphens-here"}, "name=n,alias=with-hyphens-here"},
	}
	for _, c := range cases {
		if got := networkFlagValue(c.in); got != c.want {
			t.Errorf("networkFlagValue(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestCreateService_CommandShellSplit proves the command string is
// shell-split into individual argv tokens, not passed as a single literal
// argument. Without this, docker would interpret the whole string as the
// binary path and exit immediately — the failure mode that hit
// openpanel-redis with `command: redis-server --maxmemory-policy noeviction`.
func TestCreateService_CommandShellSplit(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	err := c.CreateService(context.Background(), ServiceCreateOpts{
		ProjectName: "openpanel", ServiceName: "redis", DeploymentNumber: 1,
		Image:   "redis:7.2.5-alpine",
		Command: "redis-server --maxmemory-policy noeviction",
	})
	if err != nil {
		t.Fatalf("CreateService: %v", err)
	}
	args := r.lastCall().Args

	// The image must be followed by THREE tokens (redis-server, --maxmemory-policy, noeviction),
	// not a single concatenated string.
	for i, a := range args {
		if a == "redis:7.2.5-alpine" {
			if i+3 >= len(args) {
				t.Fatalf("expected 3 command tokens after image, got %v", args[i:])
			}
			if args[i+1] != "redis-server" || args[i+2] != "--maxmemory-policy" || args[i+3] != "noeviction" {
				t.Errorf("command tokens after image: got %v, want [redis-server --maxmemory-policy noeviction]", args[i+1:i+4])
			}
			return
		}
	}
	t.Fatalf("image not found in args: %v", args)
}

// TestCreateService_CommandWithSingleQuotes covers the openpanel-api
// shape: `sh -c 'multi-word command with operators'`. Single quotes must
// preserve embedded whitespace + operators so the whole quoted segment
// reaches docker as one argv element.
func TestCreateService_CommandWithSingleQuotes(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	err := c.CreateService(context.Background(), ServiceCreateOpts{
		ProjectName: "openpanel", ServiceName: "api", DeploymentNumber: 1,
		Image:   "lindesvard/openpanel-api:2",
		Command: "sh -c 'CI=true pnpm -r run migrate:deploy && pnpm start'",
	})
	if err != nil {
		t.Fatalf("CreateService: %v", err)
	}
	args := r.lastCall().Args

	for i, a := range args {
		if a == "lindesvard/openpanel-api:2" {
			if i+3 >= len(args) {
				t.Fatalf("expected 3 command tokens after image, got %v", args[i:])
			}
			if args[i+1] != "sh" || args[i+2] != "-c" {
				t.Errorf("expected [sh -c ...], got %v", args[i+1:i+3])
			}
			if args[i+3] != "CI=true pnpm -r run migrate:deploy && pnpm start" {
				t.Errorf("single-quoted segment lost shape: got %q", args[i+3])
			}
			return
		}
	}
	t.Fatalf("image not found in args: %v", args)
}

// TestCreateService_EmptyCommandOmitted documents the existing behavior
// that an empty command field doesn't add any positional arg after the
// image — the container's Dockerfile CMD is used. Most cobaltfiles
// don't set command at all.
func TestCreateService_EmptyCommandOmitted(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	err := c.CreateService(context.Background(), ServiceCreateOpts{
		ProjectName: "api", ServiceName: "web", DeploymentNumber: 1,
		Image: "ghcr.io/some/image:1",
		// no Command
	})
	if err != nil {
		t.Fatalf("CreateService: %v", err)
	}
	args := r.lastCall().Args
	// Image must be the last argv element (no command tokens follow).
	if args[len(args)-1] != "ghcr.io/some/image:1" {
		t.Errorf("expected image as last arg, got %v", args[len(args)-3:])
	}
}

func TestRemoveService_TreatsMissingAsSuccess(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerErr("service rm", errors.New("Error response from daemon: no such service: foo"))
	c := NewWithRunner(r)
	if err := c.RemoveService(context.Background(), "foo"); err != nil {
		t.Errorf("RemoveService: %v", err)
	}
}

func TestRemoveService_RealErrorsPropagate(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerErr("service rm", staticErr("connection refused"))
	c := NewWithRunner(r)
	if err := c.RemoveService(context.Background(), "foo"); err == nil {
		t.Error("expected error")
	}
}

func TestScaleService(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	if err := c.ScaleService(context.Background(), "api-7-web", 5); err != nil {
		t.Fatalf("ScaleService: %v", err)
	}
	args := r.lastCall().Args
	if !argSequence(args, "service", "scale", "api-7-web=5") {
		t.Errorf("scale args: %v", args)
	}
}

func TestListServicesForProject_Parses(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerStdout("service ls", "api-7-web\t2/2\napi-7-worker\t1/1\n")
	c := NewWithRunner(r)

	got, err := c.ListServicesForProject(context.Background(), 7)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d services, want 2", len(got))
	}
	if got[0].Name != "api-7-web" || got[0].Replicas != 2 {
		t.Errorf("[0]: %+v", got[0])
	}
	if got[1].Name != "api-7-worker" || got[1].Replicas != 1 {
		t.Errorf("[1]: %+v", got[1])
	}

	args := r.lastCall().Args
	if !argSequence(args, "--filter", "label=cobalt.project.id=7") {
		t.Errorf("filter args: %v", args)
	}
}

func TestWaitForServiceReady_Success(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerStdout("service ps api-7-web", "Running 5 minutes ago\nRunning 5 minutes ago\n")
	c := NewWithRunner(r)
	err := c.WaitForServiceReady(context.Background(), "api-7-web", 2, 5*time.Second)
	if err != nil {
		t.Errorf("WaitForServiceReady: %v", err)
	}
}

func TestWaitForServiceReady_FailFastOnShutdowns(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	// 3 shutdowns for 1 replica == fail-fast threshold.
	r.answerStdout(
		"service ps api-7-web",
		"Shutdown 1 minute ago\nFailed 30 seconds ago\nRejected 10 seconds ago\n",
	)
	c := NewWithRunner(r)
	err := c.WaitForServiceReady(context.Background(), "api-7-web", 1, time.Minute)
	if err == nil {
		t.Error("expected fail-fast error")
	}
}

func TestParseReplicas(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in  string
		out int
	}{
		// Format is "<running>/<desired>". We return desired so that
		// scale CLI output reflects what the operator just set, even
		// while swarm is still draining/ramping.
		{"3/3", 3}, // converged
		{"1/2", 2}, // ramping up: 1 running, 2 desired
		{"3/1", 1}, // draining: 3 running, 1 desired (regression case)
		{"0/0", 0},
		{"", 0},
		{"junk", 0},
	}
	for _, c := range cases {
		if got := parseReplicas(c.in); got != c.out {
			t.Errorf("parseReplicas(%q) = %d, want %d", c.in, got, c.out)
		}
	}
}
