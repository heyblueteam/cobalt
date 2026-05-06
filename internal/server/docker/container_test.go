package docker

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRun_FullArgs(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)

	in := strings.NewReader("hi\n")
	var out bytes.Buffer
	err := c.Run(context.Background(), RunOpts{
		ProjectID:        7,
		ProjectName:      "api",
		ServiceName:      "hook:deploy:start:before",
		DeploymentNumber: 5,
		ContainerName:    "api-hook-deploy-start-before.5",
		Image:            "cobalt/project-api-default:5",
		Command:          []string{"sh", "-c", "echo hello"},
		EnvVars:          map[string]string{"FOO": "bar"},
		Networks:         []string{"cobalt-project-api-5"},
		Volumes: []ServiceVolume{
			{VolumeName: "cobalt-volume-7-data", DestinationPath: "/data"},
		},
		WorkDir:     "/srv",
		ExtraParams: []string{"--add-host", "host.docker.internal:host-gateway"},
		Stdin:       in,
		Stdout:      &out,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	args := r.lastCall().Args

	for _, want := range []string{
		"run", "--rm",
		"--workdir", "/srv",
		"-i",
	} {
		if !argHas(args, want) {
			t.Errorf("missing %q in %v", want, args)
		}
	}
	if !argSequence(args, "--name", "api-hook-deploy-start-before.5") {
		t.Errorf("--name: %v", args)
	}
	if !argSequence(args, "--env", "FOO=bar") {
		t.Errorf("--env: %v", args)
	}
	if !argSequence(args, "--network", "cobalt-project-api-5") {
		t.Errorf("--network: %v", args)
	}
	if !argSequence(args, "--add-host", "host.docker.internal:host-gateway") {
		t.Errorf("extra params: %v", args)
	}

	// Image then command come last (image, then 3-element command).
	tail := args[len(args)-4:]
	wantTail := []string{"cobalt/project-api-default:5", "sh", "-c", "echo hello"}
	for i := range tail {
		if tail[i] != wantTail[i] {
			t.Errorf("trailing[%d]: got %q, want %q (full tail: %v)", i, tail[i], wantTail[i], tail)
		}
	}
}

func TestRun_NoStdinNoFlag(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	_ = c.Run(context.Background(), RunOpts{
		ProjectName: "api", ContainerName: "x", Image: "img",
	})
	if argHas(r.lastCall().Args, "-i") {
		t.Error("-i should not appear without Stdin")
	}
}

func TestRun_RequiresContainerName(t *testing.T) {
	t.Parallel()
	c := NewWithRunner(newFakeRunner())
	if err := c.Run(context.Background(), RunOpts{Image: "img"}); err == nil {
		t.Error("expected error for empty ContainerName")
	}
}

func TestRemoveContainer_TreatsMissingAsSuccess(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerErr("rm --force", staticErr("Error: No such container: foo"))
	c := NewWithRunner(r)
	if err := c.RemoveContainer(context.Background(), "foo"); err != nil {
		t.Errorf("RemoveContainer: %v", err)
	}
}

func TestContainerExists(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		out  string
		want bool
	}{
		{"present", "abc123\n", true},
		{"absent", "\n", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newFakeRunner()
			r.answerStdout("ps -a", tc.out)
			c := NewWithRunner(r)
			got, err := c.ContainerExists(context.Background(), "x")
			if err != nil {
				t.Fatalf("ContainerExists: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPullImage(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	if err := c.PullImage(context.Background(), "alpine:3"); err != nil {
		t.Fatalf("PullImage: %v", err)
	}
	args := r.lastCall().Args
	if !argSequence(args, "pull", "alpine:3") {
		t.Errorf("pull args: %v", args)
	}
	if err := c.PullImage(context.Background(), ""); err == nil {
		t.Error("PullImage(\"\"): expected error")
	}
}
