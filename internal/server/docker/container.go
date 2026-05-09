package docker

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// RunOpts describes a one-shot `docker run` invocation. Used by the deploy
// flow for hooks (before/after) and by `cobalt run` for ad-hoc commands.
type RunOpts struct {
	ProjectID        int64
	ProjectName      string
	ServiceName      string // logical service name, for the label
	DeploymentNumber int
	ContainerName    string
	Image            string
	Command          []string // argv; if Command[0] is a shell, caller is responsible for quoting
	EnvVars          map[string]string
	Networks         []string
	Volumes          []ServiceVolume
	WorkDir          string
	ExtraParams      []string // from SplitParams(extraRunParams)

	// IO. Any of these may be nil.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// TTY allocates a pseudo-TTY in the container (`docker run -t`).
	// Callers wiring a PTY-backed stdio carrier from the host side
	// (cobalt run --tty) should also set this so isatty()-style checks
	// inside the container behave as users expect.
	TTY bool
}

// Run runs a one-shot container synchronously and removes it on exit
// (--rm). Returns the runner error if the docker invocation itself failed
// or the container exited non-zero.
func (c *Client) Run(ctx context.Context, opts RunOpts) error {
	if opts.ContainerName == "" {
		return fmt.Errorf("docker.Run: ContainerName required")
	}
	args := []string{"run", "--rm", "--name", opts.ContainerName}

	for _, l := range serviceLabels(opts.ProjectID, opts.ProjectName, opts.ServiceName, opts.DeploymentNumber) {
		args = append(args, "--label", l)
	}
	for _, k := range sortedKeys(opts.EnvVars) {
		args = append(args, "--env", k+"="+opts.EnvVars[k])
	}
	for _, n := range opts.Networks {
		args = append(args, "--network", n)
	}
	for _, v := range opts.Volumes {
		args = append(args, "--mount",
			fmt.Sprintf("type=volume,source=%s,destination=%s", v.VolumeName, v.DestinationPath),
		)
	}
	if opts.WorkDir != "" {
		args = append(args, "--workdir", opts.WorkDir)
	}
	if opts.Stdin != nil {
		args = append(args, "-i")
	}
	if opts.TTY {
		args = append(args, "-t")
	}
	args = append(args, opts.ExtraParams...)
	args = append(args, opts.Image)
	args = append(args, opts.Command...)

	return c.runner.Run(ctx, args, opts.Stdin, opts.Stdout, opts.Stderr)
}

// DetachedRunOpts is a slimmed-down `docker run -d` for the
// self-upgrade helper. The full RunOpts is overkill (no project
// labels, no networks, no env-var ergonomics needed), and the helper
// has unusual requirements (host-socket mount, host-path bind mount)
// that would clutter RunOpts if added there.
type DetachedRunOpts struct {
	Name        string            // --name; required
	Image       string            // image:tag
	Command     []string          // argv after the image
	EnvVars     map[string]string
	BindMounts  []string          // pre-formatted "src:dst" or "src:dst:ro"
	ExtraParams []string          // any escape-hatch flags
}

// RunDetached spawns a detached, auto-cleanup container. Returns the
// container ID on success. Used by the daemon to launch a helper
// process that needs to outlive the daemon itself (self-upgrade flow:
// the helper restarts the cobalt service while the daemon dies, so
// it CANNOT be a child of the daemon process).
func (c *Client) RunDetached(ctx context.Context, opts DetachedRunOpts) (string, error) {
	if opts.Name == "" || opts.Image == "" {
		return "", fmt.Errorf("docker.RunDetached: Name and Image are required")
	}
	args := []string{"run", "--rm", "--detach", "--name", opts.Name}
	for _, k := range sortedKeys(opts.EnvVars) {
		args = append(args, "--env", k+"="+opts.EnvVars[k])
	}
	for _, m := range opts.BindMounts {
		args = append(args, "--volume", m)
	}
	args = append(args, opts.ExtraParams...)
	args = append(args, opts.Image)
	args = append(args, opts.Command...)

	var stdout strings.Builder
	if err := c.runner.Run(ctx, args, nil, &stdout, io.Discard); err != nil {
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
}

// InspectContainerImage returns the image reference of a running
// container by name. Used by the self-upgrade flow to capture the
// current daemon image as the rollback target before swapping.
func (c *Client) InspectContainerImage(ctx context.Context, name string) (string, error) {
	var stdout strings.Builder
	if err := c.runner.Run(ctx,
		[]string{"inspect", "--format", "{{.Config.Image}}", name},
		nil, &stdout, io.Discard,
	); err != nil {
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
}

// RemoveContainer removes a container by name with --force. Treats a
// missing container as success so callers can use it as cleanup.
func (c *Client) RemoveContainer(ctx context.Context, name string) error {
	err := c.run(ctx, "rm", "--force", name)
	if isNotFound(err) {
		return nil
	}
	return err
}

// Exec runs a command inside an existing container and streams the output.
// Used for things like the caddy_nc port-poll and `docker exec`-style
// debugging.
func (c *Client) Exec(ctx context.Context, container string, cmd []string, stdout, stderr io.Writer) error {
	args := append([]string{"exec", container}, cmd...)
	return c.runner.Run(ctx, args, nil, stdout, stderr)
}

// PullImage pulls an image reference. Used during deploys when the cobalt
// build step wants to ensure the base image is current.
func (c *Client) PullImage(ctx context.Context, ref string) error {
	if ref == "" {
		return fmt.Errorf("docker.PullImage: empty ref")
	}
	return c.run(ctx, "pull", ref)
}

// ContainerExists reports whether a container with the given name is
// present. Used to short-circuit cleanup paths.
func (c *Client) ContainerExists(ctx context.Context, name string) (bool, error) {
	out, err := c.output(ctx, "ps", "-a", "--filter", "name=^"+name+"$", "--format", "{{.ID}}")
	if err != nil {
		return false, err
	}
	return len(out) > 0, nil
}

