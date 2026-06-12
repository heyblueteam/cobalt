package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/heyblueteam/cobalt/internal/cliconfig"
	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func sampleSnap() cobaltapi.ServerStats {
	return cobaltapi.ServerStats{
		Node: "server.blue.cc",
		System: cobaltapi.SystemStats{
			CPUPercent: 48,
			CPUCount:   8,
			Load1:      1.2, Load5: 0.9, Load15: 0.8,
			MemUsedBytes:  21 << 30,
			MemTotalBytes: 32 << 30,
			UptimeSeconds: 41*86400 + 3*3600,
			Disks:         []cobaltapi.DiskStats{{Mount: "/", UsedBytes: 64 << 30, TotalBytes: 100 << 30}},
		},
		Containers: []cobaltapi.ContainerStats{
			{
				ID: "a1", Name: "api-114-web.1.x", Project: "api", Service: "web", Deployment: 114, Slot: 1,
				CPUPercent: 81.2, MemUsedBytes: 1 << 30, MemLimitBytes: 8 << 30, NetRxBytes: 1 << 30, NetTxBytes: 1 << 29,
			},
			{
				ID: "a2", Name: "api-114-web.2.y", Project: "api", Service: "web", Deployment: 114, Slot: 2,
				CPUPercent: 79.0, MemUsedBytes: 2 << 30, MemLimitBytes: 8 << 30,
			},
			{
				ID: "n1", Name: "next-50-web.1.z", Project: "next", Service: "web", Deployment: 50, Slot: 1,
				CPUPercent: 4, MemUsedBytes: 400 << 20,
			},
			{ID: "d1", Name: "caddy", CPUPercent: 1, MemUsedBytes: 90 << 20},
		},
	}
}

func TestBuildRows(t *testing.T) {
	rows := buildRows(sampleSnap(), false)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3: %+v", len(rows), rows)
	}
	api := rows[0]
	if api.Project != "api" || api.Service != "web" || len(api.Containers) != 2 {
		t.Fatalf("row 0 = %+v, want aggregated api/web", api)
	}
	if api.CPU != 81.2+79.0 {
		t.Errorf("api CPU = %v, want replica sum", api.CPU)
	}
	if api.MemUsed != 3<<30 {
		t.Errorf("api MemUsed = %d, want summed", api.MemUsed)
	}
	// Replicas share one limit; summing would fake headroom.
	if api.MemLimit != 8<<30 {
		t.Errorf("api MemLimit = %d, want per-replica limit", api.MemLimit)
	}
	if api.Containers[0].Slot != 1 || api.Containers[1].Slot != 2 {
		t.Errorf("replicas not sorted by slot: %+v", api.Containers)
	}
	if rows[1].Project != "next" || rows[2].Project != "other" {
		t.Errorf("project order = %s, %s; want next then other", rows[1].Project, rows[2].Project)
	}
	if rows[2].Service != "caddy" {
		t.Errorf("foreign container service = %q, want its name", rows[2].Service)
	}
}

func TestBuildRowsOtherSinksEvenWhenHot(t *testing.T) {
	snap := sampleSnap()
	snap.Containers[3].CPUPercent = 999 // caddy goes wild
	rows := buildRows(snap, false)
	if rows[len(rows)-1].Project != "other" {
		t.Errorf("other must stay last regardless of load: %+v", rows)
	}
}

func TestBuildRowsByMem(t *testing.T) {
	snap := sampleSnap()
	snap.Containers[2].MemUsedBytes = 20 << 30 // next out-memories api
	rows := buildRows(snap, true)
	if rows[0].Project != "next" {
		t.Errorf("byMem row 0 = %+v, want next", rows[0])
	}
}

func TestFilterAndProjects(t *testing.T) {
	rows := buildRows(sampleSnap(), false)
	if got := filterRows(rows, "api"); len(got) != 1 || got[0].Project != "api" {
		t.Errorf("filterRows(api) = %+v", got)
	}
	if got := filterRows(rows, ""); len(got) != 3 {
		t.Errorf("empty filter dropped rows: %+v", got)
	}
	if got := projectsIn(rows); strings.Join(got, ",") != "api,next,other" {
		t.Errorf("projectsIn = %v", got)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[uint64]string{
		0:                "0B",
		512:              "512B",
		1 << 10:          "1K",
		1536:             "1.5K",
		400 << 20:        "400M",
		1 << 30:          "1G",
		uint64(3) << 30:  "3G",
		uint64(21) << 30: "21G",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestFmtUptime(t *testing.T) {
	cases := map[int64]string{
		41*86400 + 3*3600: "41d 3h",
		3*3600 + 12*60:    "3h 12m",
		12 * 60:           "12m",
		0:                 "0m",
	}
	for in, want := range cases {
		if got := fmtUptime(in); got != want {
			t.Errorf("fmtUptime(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestGauge(t *testing.T) {
	if got := gauge(0.5, 10); got != "▓▓▓▓▓░░░░░" {
		t.Errorf("gauge(0.5) = %q", got)
	}
	if got := gauge(-1, 4); got != "░░░░" {
		t.Errorf("gauge(-1) = %q", got)
	}
	if got := gauge(2, 4); got != "▓▓▓▓" {
		t.Errorf("gauge(2) = %q", got)
	}
}

func TestSparkline(t *testing.T) {
	if got := sparkline([]float64{0, 50, 100}, 12); got != "▁▄█" {
		t.Errorf("sparkline = %q", got)
	}
	// Only the last `width` samples render.
	if got := sparkline([]float64{1, 2, 3, 4}, 2); len([]rune(got)) != 2 {
		t.Errorf("sparkline width = %q", got)
	}
	if got := sparkline(nil, 12); got != "" {
		t.Errorf("sparkline(nil) = %q", got)
	}
}

func TestRenderTable(t *testing.T) {
	rows := buildRows(sampleSnap(), false)
	out := renderTable(rows, map[string][]float64{"api/web": {10, 160}}, true)

	for _, want := range []string{"PROJECT", "api", "web ×2", ".1", ".2", "caddy", "▁█"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
	hidden := renderTable(rows, nil, false)
	if strings.Contains(hidden, ".1") {
		t.Errorf("replica rows shown despite showReplicas=false:\n%s", hidden)
	}
}

func TestRenderHeader(t *testing.T) {
	out := renderHeader(sampleSnap(), "live")
	for _, want := range []string{"server.blue.cc", "load 1.20 0.90 0.80", "up 41d 3h", "live", "21G/32G", "DISK / 64%"} {
		if !strings.Contains(out, want) {
			t.Errorf("header missing %q:\n%s", want, out)
		}
	}
}

// TestStatsOnceCLI exercises the full --once path: config → client →
// HTTP → render, against a canned daemon.
func TestStatsOnceCLI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/server/stats" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(sampleSnap())
	}))
	t.Cleanup(srv.Close)

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	configAt(t, tmpDir, &cliconfig.Config{
		Servers:       map[string]cliconfig.Server{"test": {Host: srv.URL, APIKey: "k"}},
		DefaultServer: "test",
	})

	run := func(args ...string) string {
		t.Helper()
		var buf bytes.Buffer
		oldStdout := output.Stdout
		output.Stdout = &buf
		defer func() { output.Stdout = oldStdout }()
		root := newRootCmd()
		root.SetArgs(args)
		if err := root.ExecuteContext(context.Background()); err != nil {
			t.Fatalf("execute %v: %v", args, err)
		}
		return buf.String()
	}

	plain := run("stats", "--once")
	for _, want := range []string{"server.blue.cc", "api", "web ×2", "caddy"} {
		if !strings.Contains(plain, want) {
			t.Errorf("--once output missing %q:\n%s", want, plain)
		}
	}

	var snap cobaltapi.ServerStats
	if err := json.Unmarshal([]byte(run("stats", "--json")), &snap); err != nil {
		t.Fatalf("--json output not valid JSON: %v", err)
	}
	if snap.Node != "server.blue.cc" || len(snap.Containers) != 4 {
		t.Errorf("--json roundtrip = %+v", snap)
	}
}

func TestStreamEndFatalVsRetry(t *testing.T) {
	m := statsModel{status: "live", history: map[string][]float64{}}

	// Auth failures and a missing endpoint can't be fixed by retrying:
	// the model must record the error and quit.
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		next, _ := m.Update(streamEndMsg{err: errors.New("denied"), code: code})
		fatal := next.(statsModel)
		if fatal.err == nil {
			t.Errorf("code %d: model.err not set, TUI would retry forever", code)
		}
	}

	// A dropped connection retries, and the reason is surfaced.
	next, _ := m.Update(streamEndMsg{err: errors.New("connection refused")})
	retry := next.(statsModel)
	if retry.err != nil {
		t.Fatalf("transient drop treated as fatal: %v", retry.err)
	}
	if retry.status != "reconnecting…" || retry.lastErr == nil {
		t.Errorf("status = %q, lastErr = %v; want reconnecting with reason", retry.status, retry.lastErr)
	}
	if view := retry.View(); !strings.Contains(view, "connection refused") {
		t.Errorf("View() hides the reconnect reason:\n%s", view)
	}

	// The next snapshot clears the stale error.
	next, _ = retry.Update(snapMsg(sampleSnap()))
	if live := next.(statsModel); live.lastErr != nil || live.status != "live" {
		t.Errorf("snapshot did not clear error state: status=%q lastErr=%v", live.status, live.lastErr)
	}
}

func TestPadTruncateRuneAware(t *testing.T) {
	// "données" is 7 runes / 8 bytes: byte counting under-pads and can
	// slice mid-rune.
	if got := pad("données", 9); utf8.RuneCountInString(got) != 9 {
		t.Errorf("pad = %q (%d runes), want 9 runes", got, utf8.RuneCountInString(got))
	}
	if got := truncate("données-réplica", 8); got != "données…" || !utf8.ValidString(got) {
		t.Errorf("truncate = %q, want %q", got, "données…")
	}
	if got := truncate("web", 8); got != "web" {
		t.Errorf("truncate short = %q", got)
	}
}

func TestStaleTickDemotesLiveOnly(t *testing.T) {
	m := statsModel{status: "live", lastSnapAt: time.Now().Add(-staleAfter - time.Second)}
	next, _ := m.Update(staleTickMsg{})
	if got := next.(statsModel).status; got != "stalled" {
		t.Errorf("status = %q, want stalled (no snapshot for >%v)", got, staleAfter)
	}

	fresh := statsModel{status: "live", lastSnapAt: time.Now()}
	next, _ = fresh.Update(staleTickMsg{})
	if got := next.(statsModel).status; got != "live" {
		t.Errorf("fresh stream demoted to %q", got)
	}

	reconnecting := statsModel{status: "reconnecting…", lastSnapAt: time.Now().Add(-time.Minute)}
	next, _ = reconnecting.Update(staleTickMsg{})
	if got := next.(statsModel).status; got != "reconnecting…" {
		t.Errorf("reconnecting overwritten to %q", got)
	}
}
