package docker

import "testing"

func TestNames(t *testing.T) {
	t.Parallel()
	cases := []struct {
		got, want string
	}{
		{ServiceName("api", 7, "web"), "api-7-web"},
		{StablePublicWebServiceName(42), "cobalt-web-42"},
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

func TestWebGeneration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		project, service string
		wantN            int
		wantOK           bool
	}{
		{"api", "api-7-web", 7, true},
		{"api", "api-114-web", 114, true},
		// hyphenated project name — projectName is known, so the split is safe.
		{"my-app", "my-app-3-web", 3, true},
		// non-web services are not web generations.
		{"api", "api-7-worker", 0, false},
		{"api", "api-7-cron-nightly", 0, false},
		// other project / unrelated names.
		{"api", "web-7-web", 0, false},
		{"api", "api-web", 0, false},
		{"api", "api-x-web", 0, false},
		{"api", "", 0, false},
		// round-trips with ServiceName.
		{"api", ServiceName("api", 42, "web"), 42, true},
	}
	for _, c := range cases {
		n, ok := WebGeneration(c.project, c.service)
		if ok != c.wantOK || n != c.wantN {
			t.Errorf("WebGeneration(%q, %q) = (%d, %v), want (%d, %v)",
				c.project, c.service, n, ok, c.wantN, c.wantOK)
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

// TestShellSplit covers cobaltfile `command:` parsing. Cobaltfiles
// describe commands in shell syntax (single & double quotes, escapes)
// because operators write them that way. Without shell-aware splitting,
// commands like `sh -c 'A && B'` are mangled into ten tokens, or commands
// like `redis-server --maxmemory-policy noeviction` are passed to docker
// as a single binary literal — which exits with "executable file not found".
//
// Disco parity: utils/docker.py uses Python's shlex.split for the same
// purpose.
func TestShellSplit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want []string
	}{
		// Trivial cases.
		{"empty", "", nil},
		{"whitespace_only", "   \t\n  ", nil},
		{"single_token", "redis-server", []string{"redis-server"}},
		// Bare-word commands (the common cobaltfile shape).
		{
			"flag_with_value", "redis-server --maxmemory-policy noeviction",
			[]string{"redis-server", "--maxmemory-policy", "noeviction"},
		},
		{
			"collapses_whitespace", "  redis-server   --maxmemory-policy   noeviction  ",
			[]string{"redis-server", "--maxmemory-policy", "noeviction"},
		},
		// Single-quoted: contents are literal, no escape interpretation.
		{
			"single_quoted_keeps_spaces", "sh -c 'pnpm migrate:deploy && pnpm start'",
			[]string{"sh", "-c", "pnpm migrate:deploy && pnpm start"},
		},
		{
			"single_quoted_with_env_assignment", "sh -c 'CI=true pnpm -r run migrate:deploy && pnpm start'",
			[]string{"sh", "-c", "CI=true pnpm -r run migrate:deploy && pnpm start"},
		},
		{"single_quoted_with_backslash_literal", `echo 'a\b'`, []string{"echo", `a\b`}},
		// Double-quoted: contents kept together but backslash escapes work.
		{
			"double_quoted_keeps_spaces", `sh -c "echo hello world"`,
			[]string{"sh", "-c", "echo hello world"},
		},
		{"double_quoted_escape", `echo "a\"b"`, []string{"echo", `a"b`}},
		// Backslash outside quotes — escape the next char.
		{"backslash_escape_space", `a\ b`, []string{"a b"}},
		// Mixed: token concatenation across quote modes.
		{"quoted_concat", `a'b c'd`, []string{"ab cd"}},
		// Empty quoted segments produce empty tokens.
		{"empty_single_quote", `a '' b`, []string{"a", "", "b"}},
		// Trailing backslash at end-of-input has no next char to escape;
		// fall through to default and write the `\` literally. Matches
		// shlex's non-POSIX mode (POSIX would error). Pinned by test so
		// a future "fix" doesn't silently change behavior.
		{"trailing_backslash_unquoted", `foo\`, []string{`foo\`}},
		{"trailing_backslash_inside_double_quote", `"foo\`, []string{`foo\`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ShellSplit(tc.in)
			if !slicesEqual(got, tc.want) {
				t.Errorf("ShellSplit(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func slicesEqual(a, b []string) bool {
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

// The main network must come first: docker resolves the first --network as
// the container's network mode without waiting for a swarm-scoped overlay to
// be realized on the node, so a per-deployment overlay in that slot races its
// own realization and fails container creation.
func TestOneShotNetworksPutsMainFirst(t *testing.T) {
	t.Parallel()
	got := OneShotNetworks("cobalt-main", "cobalt-project-api-484")
	want := []string{"cobalt-main", "cobalt-project-api-484"}
	if !equalStrings(got, want) {
		t.Errorf("OneShotNetworks: got %v, want %v", got, want)
	}
	if got[0] != "cobalt-main" {
		t.Errorf("main network must be first for the network mode to resolve, got %q", got[0])
	}
}
