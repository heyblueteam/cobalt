package main

import (
	"bytes"
	"context"
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
