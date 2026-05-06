package main

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func TestVolumesList(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond([]cobaltapi.Volume{
		{Name: "data", FullName: "cobalt-volume-7-data"},
		{Name: "uploads", FullName: "cobalt-volume-7-uploads"},
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"volumes", "list", "--project", "api"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(buf.String(), "data") || !contains(buf.String(), "uploads") {
		t.Errorf("missing volumes: %s", buf.String())
	}
}

func TestVolumesListJSON(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond([]cobaltapi.Volume{{Name: "data", FullName: "cobalt-volume-7-data"}})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"volumes", "list", "--project", "api", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("expected JSON output")
	}
}

func TestVolumesExport(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte{0x1f, 0x8b})
	}

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"volumes", "export", "--project", "api", "--volume", "data", "--force"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
}

func TestVolumesImport(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.handler = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"volumes", "import", "--project", "api", "--volume", "data", "--input", "/dev/null"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
}

func TestVolumesExportMissingVolume(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"volumes", "export", "--project", "api"})
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error for missing --volume")
	}
}

func TestVolumesImportMissingVolume(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"volumes", "import", "--project", "api"})
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error for missing --volume")
	}
}
