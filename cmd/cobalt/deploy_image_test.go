package main

import (
	"strings"
	"testing"
)

func TestParseImageFlags(t *testing.T) {
	ref := "ghcr.io/heyblueteam/blue-server@sha256:" + strings.Repeat("e", 64)
	got, err := parseImageFlags([]string{ref, "docs=" + ref})
	if err != nil {
		t.Fatal(err)
	}
	if got["default"] != ref || got["docs"] != ref || len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	if got, err := parseImageFlags(nil); err != nil || got != nil {
		t.Fatalf("no flags: %v %v", got, err)
	}
	for _, bad := range [][]string{
		{"ghcr.io/heyblueteam/blue-server:latest"},
		{ref, "default=" + ref},
		{"bad name=" + ref},
	} {
		if _, err := parseImageFlags(bad); err == nil {
			t.Errorf("%v: expected error", bad)
		}
	}
}

func TestShortImageDigests(t *testing.T) {
	ref := "ghcr.io/x@sha256:" + strings.Repeat("f", 64)
	if got := shortImageDigests(map[string]string{"default": ref}); got != "sha256:ffffffffffff" {
		t.Errorf("got %q", got)
	}
	if got := shortImageDigests(nil); got != "built" {
		t.Errorf("got %q", got)
	}
}
