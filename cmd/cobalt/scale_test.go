package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func TestScaleList(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond(&cobaltapi.ScaleInfo{
		Project: "api",
		Services: []cobaltapi.ScaleService{
			{Name: "web", Replicas: 3},
			{Name: "worker", Replicas: 2},
		},
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"scale", "list", "--project", "api"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := buf.String()
	if !contains(got, "web") || !contains(got, "3") {
		t.Errorf("missing web=3: %s", got)
	}
	if !contains(got, "worker") || !contains(got, "2") {
		t.Errorf("missing worker=2: %s", got)
	}
}

func TestScaleListJSON(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond(&cobaltapi.ScaleInfo{
		Project:  "api",
		Services: []cobaltapi.ScaleService{{Name: "web", Replicas: 1}},
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"scale", "list", "--project", "api", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("expected JSON output")
	}
}

func TestScaleSet(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond(&cobaltapi.ScaleInfo{
		Project: "api",
		Services: []cobaltapi.ScaleService{
			{Name: "web", Replicas: 5},
			{Name: "worker", Replicas: 2},
		},
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"scale", "set", "web=5", "worker=2", "--project", "api"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
}

func TestScaleSetInvalid(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"scale", "set", "invalid", "--project", "api"})
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid arg format")
	}
}

func TestScaleSetBadNumber(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"scale", "set", "web=notanumber", "--project", "api"})
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error for non-numeric value")
	}
}

func TestScaleSetNoArgs(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"scale", "set", "--project", "api"})
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error for no args")
	}
}
