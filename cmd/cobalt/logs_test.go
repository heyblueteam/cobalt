package main

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/heyblueteam/cobalt/internal/output"
)

func TestLogsSSEOutput(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		w.Write([]byte("data: log line 1\n\n"))
		w.Write([]byte("data: log line 2\n\n"))
		if flusher != nil {
			flusher.Flush()
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
	root.SetArgs([]string{"logs", "--project", "api"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := buf.String()
	if !contains(got, "log line 1") || !contains(got, "log line 2") {
		t.Errorf("missing log lines: %s", got)
	}
}

func TestLogsWithService(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: worker log\n\n"))
	}

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"logs", "--project", "api", "--service", "worker"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(api.lastQuery, "service=worker") {
		t.Errorf("query missing service: %s", api.lastQuery)
	}
}

func TestLogsError(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.handler = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"project not found"}`))
	}

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"logs", "--project", "api"})
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}
