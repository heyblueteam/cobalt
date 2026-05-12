package main

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/heyblueteam/cobalt/internal/output"
)

func TestMetaInfo(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond(map[string]any{
		"version":    "0.1.0",
		"hostname":   "cobalt-host",
		"uptimeSecs": int64(3600),
		"startedAt":  int64(1700000000),
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"meta", "info"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(buf.String(), "0.1.0") {
		t.Errorf("missing version: %s", buf.String())
	}
	if !contains(buf.String(), "cobalt-host") {
		t.Errorf("missing hostname: %s", buf.String())
	}
}

func TestMetaInfoJSON(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond(map[string]any{"version": "0.1.0", "uptimeSecs": int64(0), "startedAt": int64(0)})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"meta", "info", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("expected JSON output")
	}
}

func TestMetaUpgrade_NoImage_ExitsNonZero(t *testing.T) {
	// `cobalt meta upgrade` is the deprecated alias for `cobalt upgrade`
	// and now requires --image (matches the canonical command's flag
	// surface). Calling it bare must error so scripts don't read
	// silence as success.
	api := newMockAPI()
	defer api.close()

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"meta", "upgrade"})
	root.SilenceErrors = true
	root.SilenceUsage = true
	if err := root.ExecuteContext(context.Background()); err == nil {
		t.Error("expected non-nil error from `cobalt meta upgrade`")
	}
}

// TestMetaUpgrade_WithImage_DelegatesToServerUpgrade asserts the
// deprecated alias actually triggers /api/server/upgrade now (instead
// of returning the old "not implemented" stub), warns about the
// deprecation, and uses --no-follow so the test doesn't have to mock
// the SSE stream.
func TestMetaUpgrade_WithImage_DelegatesToServerUpgrade(t *testing.T) {
	var (
		gotInfo    bool
		gotUpgrade bool
	)
	api := newMockAPI()
	defer api.close()
	api.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/meta/info":
			gotInfo = true
			_, _ = w.Write([]byte(`{"version":"dev","hostname":"h","uptimeSecs":1,"startedAt":1}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/server/upgrade":
			gotUpgrade = true
			_, _ = w.Write([]byte(`{"id":"u1","status":"queued","image":"ghcr.io/x:v1"}`))
		default:
			http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"meta", "upgrade", "--image", "ghcr.io/x:v1", "--no-follow"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !gotInfo {
		t.Error("preflight GET /api/meta/info was not issued")
	}
	if !gotUpgrade {
		t.Error("POST /api/server/upgrade was not issued")
	}
	if !bytes.Contains(api.lastBody, []byte(`"image":"ghcr.io/x:v1"`)) {
		t.Errorf("upgrade body missing image: %q", api.lastBody)
	}
	if !contains(buf.String(), "deprecated") {
		t.Errorf("expected deprecation notice in output, got: %q", buf.String())
	}
	if !contains(buf.String(), "Upgrade u1 started") {
		t.Errorf("expected upgrade-started line, got: %q", buf.String())
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		secs int64
		want string
	}{
		{0, "0s"},
		{45, "45s"},
		{90, "1m30s"},
		{3600, "1h0m"},
		{3661, "1h1m"},
		{86400, "1d0h"},
		{90000, "1d1h"},
	}
	for _, tt := range tests {
		got := formatDuration(tt.secs)
		if got != tt.want {
			t.Errorf("formatDuration(%d): got %q, want %q", tt.secs, got, tt.want)
		}
	}
}
