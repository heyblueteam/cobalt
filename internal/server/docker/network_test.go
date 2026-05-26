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

func TestListNetworksForProject_Parses(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerStdout(
		"network ls --filter label=cobalt.project.id=7",
		"cobalt-project-api-3\n"+
			"cobalt-project-api-4\n"+
			"cobalt-project-api-7\n",
	)
	c := NewWithRunner(r)
	got, err := c.ListNetworksForProject(context.Background(), 7, "api")
	if err != nil {
		t.Fatalf("ListNetworksForProject: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d networks, want 3", len(got))
	}
	if got[0].Name != "cobalt-project-api-3" || got[0].DeploymentNumber != 3 {
		t.Errorf("[0]: %+v", got[0])
	}
	if got[2].DeploymentNumber != 7 {
		t.Errorf("[2]: %+v", got[2])
	}
}

// Project names with hyphens (e.g. "dev-api", "white-label") are common —
// the suffix-extract must use the full project-name prefix, not split on '-'.
func TestListNetworksForProject_HyphenatedProjectName(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerStdout(
		"network ls --filter label=cobalt.project.id=42",
		"cobalt-project-dev-api-501\n"+
			"cobalt-project-dev-api-502\n",
	)
	c := NewWithRunner(r)
	got, err := c.ListNetworksForProject(context.Background(), 42, "dev-api")
	if err != nil {
		t.Fatalf("ListNetworksForProject: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
	if got[0].DeploymentNumber != 501 || got[1].DeploymentNumber != 502 {
		t.Errorf("got %+v", got)
	}
}

// Networks that don't match the cobalt-project-{name}-{n} naming convention
// must be skipped, even when present in the label-filtered output. Belt
// and suspenders: in practice the label filter alone should exclude these,
// but we don't trust upstream to label every cobalt resource correctly
// forever.
func TestListNetworksForProject_SkipsNonMatching(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerStdout(
		"network ls --filter label=cobalt.project.id=7",
		"cobalt-project-api-3\n"+
			"cobalt-project-api-notanumber\n"+ // bad suffix
			"cobalt-other-api-5\n"+ // wrong prefix
			"cobalt-project-otherproject-9\n"+ // different project
			"\n"+ // empty line
			"cobalt-project-api-4\n",
	)
	c := NewWithRunner(r)
	got, _ := c.ListNetworksForProject(context.Background(), 7, "api")
	if len(got) != 2 {
		t.Fatalf("got %d, want 2: %+v", len(got), got)
	}
	if got[0].DeploymentNumber != 3 || got[1].DeploymentNumber != 4 {
		t.Errorf("got %+v", got)
	}
}

func TestListNetworksForProject_Empty(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerStdout("network ls --filter label=cobalt.project.id=7", "")
	c := NewWithRunner(r)
	got, err := c.ListNetworksForProject(context.Background(), 7, "api")
	if err != nil {
		t.Fatalf("ListNetworksForProject: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestRemoveNetwork(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	c := NewWithRunner(r)
	if err := c.RemoveNetwork(context.Background(), "cobalt-project-api-3"); err != nil {
		t.Fatalf("RemoveNetwork: %v", err)
	}
	if !argSequence(r.lastCall().Args, "network", "rm", "cobalt-project-api-3") {
		t.Errorf("args: %v", r.lastCall().Args)
	}
}

func TestRemoveNetwork_TreatsMissingAsSuccess(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerErr("network rm", staticErr("Error: No such network: foo"))
	c := NewWithRunner(r)
	if err := c.RemoveNetwork(context.Background(), "foo"); err != nil {
		t.Errorf("RemoveNetwork: %v (should treat missing as success)", err)
	}
}

// Active endpoints (a service still attached) is the race-protection path
// that matters most: if a deploy is mid-flight at sweep time, docker refuses
// the remove and we want the error surfaced (not swallowed) so the caller
// log-and-skips for next sweep.
func TestRemoveNetwork_ActiveEndpointsErrors(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerErr("network rm", staticErr("Error response from daemon: error while removing network: network cobalt-project-api-3 has active endpoints"))
	c := NewWithRunner(r)
	if err := c.RemoveNetwork(context.Background(), "cobalt-project-api-3"); err == nil {
		t.Error("expected error for active-endpoints case (not 'not found')")
	}
}
