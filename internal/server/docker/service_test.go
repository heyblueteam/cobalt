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
		Networks:          []string{"cobalt-project-api-3"},
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
	if args[len(args)-2] != "cobalt/project-api-default:3" {
		t.Errorf("image position wrong: %v", args)
	}
	if args[len(args)-1] != "node server.js" {
		t.Errorf("command position wrong: %v", args)
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
	r.answerStdout("service ps api-7-web",
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
		{"3/3", 3},
		{"1/2", 1},
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
