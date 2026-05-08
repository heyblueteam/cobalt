// Package docker shells out to the docker CLI. It does not link against the
// docker SDK — every operation is a `docker ...` invocation, captured and
// surfaced to callers via a Runner interface so tests can assert args
// without requiring a docker daemon.
//
// Every resource cobalt creates carries two labels: cobalt.project.id (used
// for every internal lookup / filter) and cobalt.project.name (display only,
// for `docker ps --filter`). See docs/architecture.md.
package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// Runner abstracts the docker shell-out so tests can substitute a fake.
type Runner interface {
	// Run invokes the docker CLI with args, optionally feeding stdin and
	// capturing stdout/stderr to the provided writers. If a writer is nil
	// the corresponding stream is discarded.
	//
	// On a non-zero exit, Run returns an error. If the underlying error
	// is a *exec.ExitError, the wrapper preserves that via errors.Is /
	// errors.As so callers can recover the real exit code; see
	// docker.ExitCode.
	Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error
	// RunWithEnv is like Run but adds the supplied key=value pairs to
	// the docker CLI subprocess's environment. Used for `buildx build
	// --secret id=KEY,env=KEY` so buildkit can resolve KEY against the
	// subprocess env without the daemon's process inheriting per-build
	// secrets.
	RunWithEnv(ctx context.Context, env map[string]string, args []string, stdin io.Reader, stdout, stderr io.Writer) error
}

// ExitCode unwraps err and returns the exit status of the last docker
// invocation. Returns -1 if the error doesn't carry an exit code (e.g.
// "docker not found", context canceled before exec, fake runner returning
// an arbitrary error). Used by `cobalt run` to plumb the container's
// real exit status back to the CLI shell.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// ExecRunner is the production Runner: shells out to /usr/bin/docker.
type ExecRunner struct {
	// Path is the docker binary to invoke. Empty means "docker" on PATH.
	Path string
}

// Run shells out to docker and surfaces stderr in the returned error if the
// command fails. stdout/stderr writers, when non-nil, receive the full
// streams alongside our internal capture.
func (r *ExecRunner) Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	return r.RunWithEnv(ctx, nil, args, stdin, stdout, stderr)
}

// RunWithEnv is like Run plus extra env vars merged into the docker CLI
// subprocess environment. Used by Build() to pass each project env-var
// secret value through to buildkit.
func (r *ExecRunner) RunWithEnv(ctx context.Context, extraEnv map[string]string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	bin := r.Path
	if bin == "" {
		bin = "docker"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	// Enable BuildKit for every docker invocation. The classic builder is
	// deprecated and doesn't support --secret (which we depend on for
	// passing env vars into builds).
	cmd.Env = append(os.Environ(), "DOCKER_BUILDKIT=1")
	for k, v := range extraEnv {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if stdin != nil {
		cmd.Stdin = stdin
	}

	// We tee stderr internally so a non-zero exit produces a useful error
	// message even when the caller hasn't supplied a stderr writer.
	var stderrBuf bytes.Buffer
	if stderr != nil {
		cmd.Stderr = io.MultiWriter(stderr, &stderrBuf)
	} else {
		cmd.Stderr = &stderrBuf
	}
	if stdout != nil {
		cmd.Stdout = stdout
	}

	if err := cmd.Run(); err != nil {
		// Trim noise; docker errors are typically a single line.
		msg := bytes.TrimSpace(stderrBuf.Bytes())
		if len(msg) > 0 {
			return fmt.Errorf("docker %s: %w: %s", args[0], err, msg)
		}
		return fmt.Errorf("docker %s: %w", args[0], err)
	}
	return nil
}

// BuildxBuilderName is the buildx instance the daemon creates and uses
// for every project build. We need a docker-container driver (not the
// default docker driver) so --cache-to type=local works for per-project
// BuildKit cache isolation.
const BuildxBuilderName = "cobalt-builder"

// Client is the user-facing handle for everything in this package.
type Client struct {
	runner Runner
}

// New returns a Client that shells out to /usr/bin/docker via ExecRunner.
func New() *Client {
	return &Client{runner: &ExecRunner{}}
}

// NewWithRunner returns a Client backed by a custom Runner. Tests use this
// with a fake runner; production callers want New.
func NewWithRunner(r Runner) *Client {
	return &Client{runner: r}
}

// run is a small shorthand: stdin/stdout/stderr all go to nil writers and
// the only thing the caller cares about is success/failure.
func (c *Client) run(ctx context.Context, args ...string) error {
	return c.runner.Run(ctx, args, nil, nil, nil)
}

// EnsureBuildxBuilder makes sure the docker-container builder named by
// BuildxBuilderName exists, creating it if needed. Idempotent — safe to
// call on every daemon boot.
//
// The docker-container driver is required so that --cache-to type=local
// works for per-project cache isolation (improvement E in §8b).
func (c *Client) EnsureBuildxBuilder(ctx context.Context) error {
	if err := c.run(ctx, "buildx", "inspect", BuildxBuilderName); err == nil {
		return nil
	}
	return c.run(ctx, "buildx", "create",
		"--name", BuildxBuilderName,
		"--driver", "docker-container",
		"--bootstrap",
	)
}

// output captures stdout into a buffer and returns it, trimmed.
func (c *Client) output(ctx context.Context, args ...string) ([]byte, error) {
	var buf bytes.Buffer
	if err := c.runner.Run(ctx, args, nil, &buf, nil); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}
