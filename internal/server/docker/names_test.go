package docker

import "testing"

func TestNames(t *testing.T) {
	t.Parallel()
	cases := []struct {
		got, want string
	}{
		{ServiceName("api", 7, "web"), "api-7-web"},
		{NetworkName("api", 7), "cobalt-project-api-7"},
		{HookContainerName("api", "hook:deploy:start:before", 7), "api-hook-deploy-start-before.7"},
		{RunContainerName("api", 42), "api-run.42"},
		{InternalImageName("api", "default", 7), "cobalt/project-api-default:7"},
		{InternalImagePrefix("api"), "cobalt/project-api-"},
		{VolumeName(5, "data"), "cobalt-volume-5-data"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

func TestSplitParams(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"--foo", []string{"--foo"}},
		{"--foo bar", []string{"--foo", "bar"}},
		{"  --foo   bar  ", []string{"--foo", "bar"}},
		{"--host host.docker.internal:host-gateway", []string{"--host", "host.docker.internal:host-gateway"}},
	}
	for _, c := range cases {
		got := SplitParams(c.in)
		if len(got) != len(c.want) {
			t.Errorf("SplitParams(%q): got %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("SplitParams(%q)[%d]: got %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestLabels(t *testing.T) {
	t.Parallel()
	got := serviceLabels(7, "api", "web", 3)
	want := []string{
		"cobalt.project.id=7",
		"cobalt.project.name=api",
		"cobalt.service.name=web",
		"cobalt.deployment.number=3",
	}
	if len(got) != len(want) {
		t.Fatalf("len: %d vs %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestFilters(t *testing.T) {
	t.Parallel()
	got := FilterByProjectID(7)
	wantProj := []string{"label=cobalt.project.id=7"}
	if !equalStrings(got, wantProj) {
		t.Errorf("FilterByProjectID: got %v, want %v", got, wantProj)
	}
	// Each filter must arrive as its own --filter flag — Docker CLI
	// does not AND a comma-joined `label=X,label=Y` form.
	got = FilterByDeployment(7, 3)
	want := []string{
		"label=cobalt.project.id=7",
		"label=cobalt.deployment.number=3",
	}
	if !equalStrings(got, want) {
		t.Errorf("FilterByDeployment: got %v, want %v", got, want)
	}
}

func TestWithFilterFlags(t *testing.T) {
	t.Parallel()
	got := withFilterFlags([]string{"service", "ls"}, []string{"label=A=1", "label=B=2"})
	want := []string{"service", "ls", "--filter", "label=A=1", "--filter", "label=B=2"}
	if !equalStrings(got, want) {
		t.Errorf("withFilterFlags: got %v, want %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
