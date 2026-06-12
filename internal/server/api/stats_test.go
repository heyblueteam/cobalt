package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/deploy"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// fakeSysSampler counts calls so tests can observe the prime-then-measure
// pattern: the snapshot's CPUPercent equals the call ordinal.
type fakeSysSampler struct{ calls int }

func (f *fakeSysSampler) Sample() (cobaltapi.SystemStats, error) {
	f.calls++
	return cobaltapi.SystemStats{
		CPUPercent:    float64(f.calls),
		CPUCount:      8,
		MemUsedBytes:  10 << 30,
		MemTotalBytes: 32 << 30,
	}, nil
}

// statsRunner answers the two docker invocations the stats endpoint
// makes. Everything else returns empty output.
type statsRunner struct{}

const statsBody = `{"ID":"aaa","Name":"api-114-web.3.x9y","CPUPerc":"81.20%","MemUsage":"1.5GiB / 8GiB","NetIO":"1.2GB / 800MB","BlockIO":"0B / 0B","PIDs":"23"}
{"ID":"ddd","Name":"caddy","CPUPerc":"1.00%","MemUsage":"90MiB / 31GiB","NetIO":"0B / 0B","BlockIO":"0B / 0B","PIDs":"7"}
`

const psBody = "aaa\tapi-114-web.3.x9y\tapi\tweb\t114\tapi-114-web\n" +
	"ddd\tcaddy\t\t\t\t\n"

func (statsRunner) Run(_ context.Context, args []string, _ io.Reader, stdout, _ io.Writer) error {
	if stdout == nil {
		return nil
	}
	switch args[0] {
	case "stats":
		_, _ = io.WriteString(stdout, statsBody)
	case "ps":
		_, _ = io.WriteString(stdout, psBody)
	}
	return nil
}

func (r statsRunner) RunWithEnv(ctx context.Context, _ map[string]string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	return r.Run(ctx, args, stdin, stdout, stderr)
}

func newStatsEnv(t *testing.T) (*httptest.Server, *fakeSysSampler) {
	t.Helper()
	db := openTestDB(t)
	sys := &fakeSysSampler{}
	mux := http.NewServeMux()
	h := &Handler{
		DB:     db,
		Queue:  deploy.NewQueue(db),
		Docker: docker.NewWithRunner(statsRunner{}),
		Sys:    func() SystemSampler { return sys },
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, sys
}

func TestServerStats_OneShot(t *testing.T) {
	t.Parallel()
	srv, sys := newStatsEnv(t)

	resp, err := srv.Client().Get(srv.URL + "/api/server/stats")
	if err != nil {
		t.Fatal(err)
	}
	mustStatus(t, resp, http.StatusOK)
	snap := decode[cobaltapi.ServerStats](t, resp)

	if snap.Node == "" {
		t.Error("Node is empty")
	}
	if snap.SampledAt.IsZero() {
		t.Error("SampledAt is zero")
	}
	// Sample is called twice: once to prime the CPU delta, once for the
	// snapshot — so the reported value is the second call's.
	if snap.System.CPUPercent != 2 || sys.calls != 2 {
		t.Errorf("CPUPercent = %v (calls=%d), want prime+measure", snap.System.CPUPercent, sys.calls)
	}
	if len(snap.Containers) != 2 {
		t.Fatalf("got %d containers, want 2: %+v", len(snap.Containers), snap.Containers)
	}
	web := snap.Containers[0]
	if web.Project != "api" || web.Service != "web" || web.Deployment != 114 || web.Slot != 3 {
		t.Errorf("attribution = %+v", web)
	}
	if web.CPUPercent != 81.2 || web.MemLimitBytes != 8<<30 {
		t.Errorf("usage = %+v", web)
	}
	if other := snap.Containers[1]; other.Project != "" || other.Name != "caddy" {
		t.Errorf("foreign container = %+v", other)
	}
}

func TestServerStats_Follow(t *testing.T) {
	t.Parallel()
	srv, _ := newStatsEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/server/stats?follow=1", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	mustStatus(t, resp, http.StatusOK)
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}

	// The fake docker stats exits immediately after its emits, so the
	// handler sends the initial snapshot plus the final stream-died one,
	// then closes — exactly two events, no timing involved.
	var events []cobaltapi.ServerStats
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var s cobaltapi.ServerStats
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &s); err != nil {
			t.Fatalf("bad event %q: %v", line, err)
		}
		events = append(events, s)
	}
	if len(events) < 2 {
		t.Fatalf("got %d events, want ≥2", len(events))
	}
	last := events[len(events)-1]
	if len(last.Containers) != 2 {
		t.Fatalf("final snapshot has %d containers, want 2", len(last.Containers))
	}
	if last.Containers[0].Project != "api" {
		t.Errorf("attribution = %+v", last.Containers[0])
	}
}
