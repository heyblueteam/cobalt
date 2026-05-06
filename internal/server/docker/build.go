package docker

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// BuildOpts describes a single image build.
type BuildOpts struct {
	ProjectID        int64
	ProjectName      string
	ImageName        string // logical name from cobaltfile.images.<name>
	DeploymentNumber int
	Dockerfile       string // relative to Context
	Context          string // build context dir
	EnvSecrets       map[string]string
	NoCache          bool
}

// Build builds an image and tags it as InternalImageName(...).
//
// Each entry in EnvSecrets is exposed to the build via `--secret id=KEY`.
// The Dockerfile must opt in with `RUN --mount=type=secret,id=KEY ...` to
// access them. Secrets are NOT visible in image layers — this is the
// point of using --secret over --build-arg.
func (c *Client) Build(ctx context.Context, opts BuildOpts) (string, error) {
	if opts.ProjectName == "" || opts.ImageName == "" {
		return "", fmt.Errorf("docker.Build: ProjectName and ImageName required")
	}

	tag := InternalImageName(opts.ProjectName, opts.ImageName, opts.DeploymentNumber)
	args := []string{"build", "-t", tag}
	if opts.Dockerfile != "" {
		args = append(args, "-f", opts.Dockerfile)
	}
	if opts.NoCache {
		args = append(args, "--no-cache")
	}
	for _, label := range serviceLabels(opts.ProjectID, opts.ProjectName, opts.ImageName, opts.DeploymentNumber) {
		args = append(args, "--label", label)
	}

	// Sort secret keys so the argv we generate is deterministic — matters
	// for tests, build cache layer reuse, and human readers.
	keys := make([]string, 0, len(opts.EnvSecrets))
	for k := range opts.EnvSecrets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--secret", "id="+k)
	}

	contextDir := opts.Context
	if contextDir == "" {
		contextDir = "."
	}
	args = append(args, contextDir)

	if err := c.run(ctx, args...); err != nil {
		return "", fmt.Errorf("docker.Build %s: %w", tag, err)
	}
	return tag, nil
}

// SplitParams parses an extraSwarmParams / extraRunParams string into argv
// fragments. Whitespace-only or empty input returns nil. Internal whitespace
// runs collapse: "  --foo   bar  " → ["--foo", "bar"].
//
// Exposed so service-create and container-run paths share the same parsing.
func SplitParams(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Fields(s)
	if len(parts) == 0 {
		return nil
	}
	return parts
}
