package deploy

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// GeneratorRunner is the docker subset runGenerators needs.
type GeneratorRunner interface {
	Run(ctx context.Context, opts docker.RunOpts) error
}

// runGenerators executes every cobaltfile service of type=generator. Each
// runs as a one-shot container with the deployment's static target dir
// bind-mounted at /output. The generator's command writes to /output;
// after the container exits, files are on the host ready for Caddy's
// file_server to serve via ServeStaticSite.
//
// Returns the static target dir (consumed by the swap step). If multiple
// generators exist, all output to the same /output dir — order isn't
// defined; cobalt.json should declare at most one generator.
func runGenerators(
	ctx context.Context,
	d GeneratorRunner,
	project store.Project,
	dep store.Deployment,
	built []BuiltService,
	envVars map[string]string,
	staticDir string,
	stdout, stderr io.Writer,
) (string, error) {
	createdDir := false

	for _, b := range built {
		if b.Service.Type != cobaltfile.TypeGenerator {
			continue
		}
		if b.Service.Command == "" {
			return "", fmt.Errorf("deploy.runGenerators: %q is type=generator but has no command", b.Name)
		}
		if !createdDir {
			if err := os.MkdirAll(staticDir, 0o755); err != nil {
				return "", fmt.Errorf("deploy.runGenerators: mkdir %s: %w", staticDir, err)
			}
			createdDir = true
		}
		opts := docker.RunOpts{
			ProjectID:        project.ID,
			ProjectName:      project.Name,
			ServiceName:      b.Name,
			DeploymentNumber: dep.Number,
			ContainerName:    docker.ServiceName(project.Name, dep.Number, b.Name) + "-gen",
			Image:            b.ImageTag,
			Command:          []string{"sh", "-c", b.Service.Command},
			EnvVars:          envVars,
			Networks:         []string{MainNetworkName},
			ExtraParams:      docker.SplitParams(b.Service.ExtraRunParams),
			// Bind-mount the host output dir so generated files survive
			// container exit. We can't use ServiceVolume (named volumes)
			// here — Caddy needs to read directly from the host path.
			Volumes: []docker.ServiceVolume{
				{VolumeName: staticDir, DestinationPath: "/output"},
			},
			WorkDir: "/output",
			Stdout:  stdout,
			Stderr:  stderr,
		}
		if err := d.Run(ctx, opts); err != nil {
			return "", fmt.Errorf("deploy.runGenerators %q: %w", b.Name, err)
		}
	}

	if !createdDir {
		return "", nil // no generator declared
	}
	return staticDir, nil
}
