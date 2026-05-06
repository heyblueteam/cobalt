package docker

import (
	"context"
	"testing"
)

func TestCreateNetwork(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	if err := c.CreateNetwork(context.Background(), 7, "api", 3); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}
	args := r.lastCall().Args
	for _, want := range []string{
		"network", "create",
		"--driver", "overlay",
		"--attachable",
		"--opt", "encrypted",
	} {
		if !argHas(args, want) {
			t.Errorf("missing %q in %v", want, args)
		}
	}
	if args[len(args)-1] != "cobalt-project-api-3" {
		t.Errorf("network name: got %q", args[len(args)-1])
	}
	for _, want := range []string{
		"cobalt.project.id=7",
		"cobalt.project.name=api",
		"cobalt.deployment.number=3",
	} {
		if !argHas(args, want) {
			t.Errorf("missing label %q", want)
		}
	}
}

func TestNetworkExists(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerStdout("network ls --filter name=^cobalt-project-api-3$", "cobalt-project-api-3\n")
	c := NewWithRunner(r)
	got, err := c.NetworkExists(context.Background(), "cobalt-project-api-3")
	if err != nil {
		t.Fatalf("NetworkExists: %v", err)
	}
	if !got {
		t.Error("expected true")
	}
}

func TestConnectNetwork(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	_ = c.ConnectNetwork(context.Background(), "n", "ctr")
	if !argSequence(r.lastCall().Args, "network", "connect", "n", "ctr") {
		t.Errorf("args: %v", r.lastCall().Args)
	}
}
