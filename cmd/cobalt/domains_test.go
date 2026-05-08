package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func TestDomainsList(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond([]cobaltapi.Domain{
		{Name: "api.blue.cc"},
		{Name: "www.api.blue.cc"},
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"domains", "list", "--project", "api"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := buf.String()
	if !contains(got, "api.blue.cc") || !contains(got, "www.api.blue.cc") {
		t.Errorf("missing domains: %s", got)
	}
}

func TestDomainsListJSON(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond([]cobaltapi.Domain{{Name: "api.blue.cc"}})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"domains", "list", "--project", "api", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("expected JSON output")
	}
}

func TestDomainsListEmpty(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond([]cobaltapi.Domain{})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"domains", "list", "--project", "api"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(buf.String(), "No domains") {
		t.Errorf("expected empty message: %s", buf.String())
	}
}

func TestDomainsAdd(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond(&cobaltapi.Domain{Name: "api.blue.cc"})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"domains", "add", "api.blue.cc", "--project", "api"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(buf.String(), "added") {
		t.Errorf("expected 'added': %s", buf.String())
	}
}

func TestDomainsRemove(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond(nil)

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"domains", "remove", "api.blue.cc", "--project", "api", "--yes"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
}

func TestApexWWWPair(t *testing.T) {
	tests := []struct {
		input    string
		wantPair string
		wantKind string
	}{
		{"example.com", "www.example.com", "apex"},
		{"blue.cc", "www.blue.cc", "apex"},
		{"www.example.com", "example.com", "www"},
		{"www.blue.cc", "blue.cc", "www"},
		{"app.example.com", "", ""},
		{"a.b.c.example.com", "", ""},
		{"localhost", "", ""},
	}
	for _, tt := range tests {
		gotPair, gotKind := apexWWWPair(tt.input)
		if gotPair != tt.wantPair || gotKind != tt.wantKind {
			t.Errorf("apexWWWPair(%q) = (%q, %q), want (%q, %q)",
				tt.input, gotPair, gotKind, tt.wantPair, tt.wantKind)
		}
	}
}
