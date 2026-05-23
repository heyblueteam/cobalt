package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/heyblueteam/cobalt/internal/output"
)

// caddyfileForInit is the decision function that drives whether (and which)
// Caddyfile gets written during `cobalt init`. SSH writes are gated on the
// returned body being non-empty. Testing this helper covers the bug fix
// without standing up a mock SSH transport.
//
// Original bug: `cobalt init --compose-file <yaml>` skipped the Caddyfile
// write entirely, even though the embedded compose template (and most
// near-identical operator variants) bind-mount /opt/cobalt/Caddyfile.
// cobalt_caddy then entered a Rejected restart loop with
// "bind source path does not exist".
func TestCaddyfileForInit(t *testing.T) {
	cases := []struct {
		name        string
		noCaddyfile bool
		host        string
		insecureTLS bool
		want        string // "auto-https", "internal", or "" (skip)
	}{
		// --compose-file + real domain → auto-https Caddyfile written
		// (the --compose-file flag isn't an input here; this helper is
		// always consulted, and the prod write-block bypasses the
		// `if composeFile != ""` gate that previously suppressed it).
		{"compose-file + real domain → auto-https", false, "cobalt.blue.cc", false, "auto-https"},

		// --compose-file + --insecure-tls → internal Caddyfile
		{"compose-file + insecure-tls → internal", false, "cobalt.blue.cc", true, "internal"},

		// --compose-file + IP host → internal (no Let's Encrypt for IPs)
		{"compose-file + IP host → internal", false, "192.168.1.100", false, "internal"},

		// --no-caddyfile → suppress entirely, regardless of host
		{"no-caddyfile + real domain", true, "cobalt.blue.cc", false, ""},
		{"no-caddyfile + insecure-tls", true, "192.168.1.100", true, ""},

		// Default path (no --compose-file, no --no-caddyfile) is also
		// driven by this helper and must keep returning the right shape.
		{"default + real domain → auto-https", false, "cobalt.blue.cc", false, "auto-https"},
		{"default + IP → internal", false, "10.0.0.5", false, "internal"},
		{"default + localhost → internal", false, "localhost", false, "internal"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := caddyfileForInit(c.noCaddyfile, c.host, c.insecureTLS)

			got := ""
			switch body {
			case "":
				got = ""
			case initCaddyfileAutoHTTPS:
				got = "auto-https"
			case initCaddyfileInternal:
				got = "internal"
			default:
				t.Fatalf("caddyfileForInit returned an unrecognized body (len=%d)", len(body))
			}

			if got != c.want {
				t.Errorf("caddyfileForInit(noCaddyfile=%v, host=%q, insecure=%v): got %q, want %q",
					c.noCaddyfile, c.host, c.insecureTLS, got, c.want)
			}
		})
	}
}

// TestInitCommand_NoCaddyfileFlag verifies the --no-caddyfile flag is wired
// onto the cobra command, since a missing flag would cause the build to fail
// but a typo in the long-flag name wouldn't.
func TestInitCommand_NoCaddyfileFlag(t *testing.T) {
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"init", "--help"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute --help: %v", err)
	}
	got := buf.String()
	if !contains(got, "--no-caddyfile") {
		t.Errorf("missing --no-caddyfile in help output:\n%s", got)
	}
	if !contains(got, "skip writing") {
		t.Errorf("--no-caddyfile help text missing 'skip writing':\n%s", got)
	}
}

// TestInitCommand_ComposeFileExample verifies the help docs surface the
// --no-caddyfile + --compose-file example, since the two flags are
// closely linked and operators discovering one should see the other.
func TestInitCommand_ComposeFileExample(t *testing.T) {
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	// Silence the info banner output the command may emit through
	// output.Stderr at help time.
	oldStderr := output.Stderr
	output.Stderr = &bytes.Buffer{}
	defer func() { output.Stderr = oldStderr }()

	root.SetArgs([]string{"init", "--help"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute --help: %v", err)
	}
	got := buf.String()
	if !contains(got, "--compose-file") {
		t.Errorf("expected --compose-file in help: %s", got)
	}
	if !contains(got, "--no-caddyfile") {
		t.Errorf("expected --no-caddyfile in help: %s", got)
	}
}

// TestInitCommand_NoCaddyfileRequiresComposeFile guards against the footgun
// of passing --no-caddyfile alone: the embedded compose template bind-mounts
// /opt/cobalt/Caddyfile, so suppressing the write reproduces the exact
// crash-loop this PR is fixing. The RunE preflight should reject it.
func TestInitCommand_NoCaddyfileRequiresComposeFile(t *testing.T) {
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	oldStderr := output.Stderr
	output.Stderr = &bytes.Buffer{}
	defer func() { output.Stderr = oldStderr }()

	root.SetArgs([]string{"init", "root@example.com", "--no-caddyfile", "--yes"})
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatalf("expected --no-caddyfile without --compose-file to error, got nil")
	}
	if !contains(err.Error(), "--no-caddyfile requires --compose-file") {
		t.Errorf("expected error to mention --no-caddyfile requires --compose-file; got %v", err)
	}
}
