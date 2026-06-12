package docker

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want uint64
	}{
		{"0B", 0},
		{"12B", 12},
		{"1.2kB", 1200},
		{"3.5MB", 3500000},
		{"1.5GB", 1500000000},
		{"532.5MiB", uint64(532.5 * (1 << 20))},
		{"7.667GiB", 8232378564}, // 7.667 × 2³⁰, truncated
		{" 1KiB ", 1024},
		{"--", 0},
		{"", 0},
		{"1.5XB", 0},
		{"garbage", 0},
	}
	for _, c := range cases {
		if got := parseSize(c.in); got != c.want {
			t.Errorf("parseSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParsePercent(t *testing.T) {
	for in, want := range map[string]float64{"3.05%": 3.05, "0.00%": 0, "312.44%": 312.44, "--": 0, "": 0} {
		if got := parsePercent(in); got != want {
			t.Errorf("parsePercent(%q) = %v, want %v", in, got, want)
		}
	}
}

// Two real-shaped emits, one ANSI-prefixed refresh, one garbage line, one
// header-ish line without JSON: only the JSON rows must survive.
const statsFixture = `{"BlockIO":"532MB / 1.2GB","CPUPerc":"81.23%","Container":"abc","ID":"abc123","MemPerc":"19.5%","MemUsage":"1.5GiB / 8GiB","Name":"api-114-web.1.x9ya","NetIO":"1.2GB / 800MB","PIDs":"23"}
` + "\x1b[2J\x1b[H" + `{"BlockIO":"0B / 0B","CPUPerc":"2.01%","ID":"def456","MemUsage":"90MiB / 31.21GiB","Name":"caddy","NetIO":"1.5kB / 0B","PIDs":"7"}
not json at all
`

func TestStatsOnce(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerStdout("stats --no-stream", statsFixture)
	c := NewWithRunner(r)

	got, err := c.StatsOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(got), got)
	}
	web := got[0]
	if web.ID != "abc123" || web.CPUPercent != 81.23 || web.PIDs != 23 {
		t.Errorf("row 0 = %+v", web)
	}
	if web.MemUsedBytes != uint64(1.5*(1<<30)) || web.MemLimitBytes != 8<<30 {
		t.Errorf("mem = %d/%d", web.MemUsedBytes, web.MemLimitBytes)
	}
	if web.NetRxBytes != 1200000000 || web.NetTxBytes != 800000000 {
		t.Errorf("net = %d/%d", web.NetRxBytes, web.NetTxBytes)
	}
	if web.BlockReadBytes != 532000000 || web.BlockWriteBytes != 1200000000 {
		t.Errorf("block = %d/%d", web.BlockReadBytes, web.BlockWriteBytes)
	}

	args := strings.Join(r.lastCall().Args, " ")
	if args != "stats --no-stream --no-trunc --format {{json .}}" {
		t.Errorf("argv = %q", args)
	}
}

func TestStatsStreamLatestWins(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	// Same container emitted twice: the second emit must win.
	r.answerStdout("stats --no-trunc", `{"ID":"abc123","Name":"api-114-web.1.x","CPUPerc":"10.00%","MemUsage":"1GiB / 8GiB","NetIO":"0B / 0B","BlockIO":"0B / 0B","PIDs":"5"}
{"ID":"abc123","Name":"api-114-web.1.x","CPUPerc":"55.00%","MemUsage":"2GiB / 8GiB","NetIO":"0B / 0B","BlockIO":"0B / 0B","PIDs":"6"}
`)
	c := NewWithRunner(r)
	s := c.StatsStream(context.Background())
	<-s.Done()

	got := s.Snapshot()
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if got[0].CPUPercent != 55 || got[0].MemUsedBytes != 2<<30 || got[0].PIDs != 6 {
		t.Errorf("latest emit did not win: %+v", got[0])
	}
}

func TestStatsStreamEviction(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	s := &StatsStream{
		latest: map[string]streamEntry{
			"fresh": {usage: ContainerUsage{ID: "fresh", Name: "a"}, seen: base},
			"stale": {usage: ContainerUsage{ID: "stale", Name: "b"}, seen: base.Add(-streamMaxAge - time.Second)},
		},
		now: func() time.Time { return base },
	}
	got := s.Snapshot()
	if len(got) != 1 || got[0].ID != "fresh" {
		t.Fatalf("Snapshot = %+v, want only the fresh container", got)
	}
	if _, ok := s.latest["stale"]; ok {
		t.Error("stale entry not pruned from the map")
	}
}

func TestContainerOwners(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	// Columns: ID, Names, cobalt.project.name, cobalt.service.name,
	// cobalt.deployment.number, com.docker.swarm.service.name.
	r.answerStdout("ps --no-trunc", strings.Join([]string{
		// New-style task: cobalt container-labels present.
		"aaa\tapi-114-web.3.x9y\tapi\tweb\t114\tapi-114-web",
		// Pre-container-label task: swarm label only.
		"bbb\twhite-label-9-web.1.z2k\t\t\t\twhite-label-9-web",
		// One-off run container: cobalt labels, no swarm name, no slot.
		"ccc\tcobalt-run-42\tapi\tweb\t114\t",
		// Not ours at all.
		"ddd\tcaddy\t\t\t\t",
		// Malformed line must be skipped, not crash.
		"junk-without-tabs",
		"",
	}, "\n"))
	c := NewWithRunner(r)

	owners, err := c.ContainerOwners(context.Background(), []string{"white", "api", "white-label"})
	if err != nil {
		t.Fatal(err)
	}

	if o := owners["aaa"]; o.Project != "api" || o.Service != "web" || o.Deployment != 114 || o.Slot != 3 {
		t.Errorf("label-based owner = %+v", o)
	}
	// Longest project name must win the prefix match.
	if o := owners["bbb"]; o.Project != "white-label" || o.Service != "web" || o.Deployment != 9 || o.Slot != 1 {
		t.Errorf("swarm-parsed owner = %+v", o)
	}
	if o := owners["ccc"]; o.Project != "api" || o.Slot != 0 {
		t.Errorf("run-container owner = %+v", o)
	}
	if o := owners["ddd"]; o.Project != "" {
		t.Errorf("foreign container attributed to %+v", o)
	}
	if _, ok := owners["junk-without-tabs"]; ok {
		t.Error("malformed line produced an owner")
	}
}

func TestResolveOwnerUnknownProject(t *testing.T) {
	t.Parallel()
	// A swarm service whose name matches no known project stays unowned —
	// e.g. a service someone created by hand on the same host.
	o := resolveOwner("foo-1-web.1.x", "", "", "", "foo-1-web", []string{"api"})
	if o != (ContainerOwner{}) {
		t.Errorf("resolveOwner = %+v, want zero", o)
	}
}
