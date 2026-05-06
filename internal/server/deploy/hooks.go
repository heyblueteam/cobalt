package deploy

import (
	"context"
	"fmt"
	"io"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// HookRunner is the docker subset Run* hook helpers need.
type HookRunner interface {
	Run(ctx context.Context, opts docker.RunOpts) error
}

// runHook executes a single one-shot hook container on the cobalt-main
// network. Returns nil if the named hook isn't declared in the cobaltfile
// (callers don't need to check first).
//
// The hook's image is the project's "default" image at the supplied
// deployment number. If a service's cobaltfile entry overrides the image,
// that's honored.
func runHook(
	ctx context.Context,
	d HookRunner,
	project store.Project,
	dep store.Deployment,
	cf *cobaltfile.Cobaltfile,
	hookName string,
	envVars map[string]string,
	stdout, stderr io.Writer,
) error {
	svc, ok := cf.Services[hookName]
	if !ok {
		return nil
	}
	if svc.Type != cobaltfile.TypeCommand {
		return fmt.Errorf("deploy.runHook: %q must be type=command (got %q)", hookName, svc.Type)
	}
	if svc.Command == "" {
		return fmt.Errorf("deploy.runHook: %q has no command", hookName)
	}

	imageTag := docker.InternalImageName(project.Name, svc.Image, dep.Number)
	containerName := docker.HookContainerName(project.Name, hookName, dep.Number)

	opts := docker.RunOpts{
		ProjectID:        project.ID,
		ProjectName:      project.Name,
		ServiceName:      hookName,
		DeploymentNumber: dep.Number,
		ContainerName:    containerName,
		Image:            imageTag,
		Command:          []string{"sh", "-c", svc.Command},
		EnvVars:          envVars,
		Networks:         []string{MainNetworkName},
		ExtraParams:      docker.SplitParams(svc.ExtraRunParams),
		Stdout:           stdout,
		Stderr:           stderr,
	}
	if err := d.Run(ctx, opts); err != nil {
		return fmt.Errorf("deploy.runHook %q: %w", hookName, err)
	}
	return nil
}

// runBeforeHook runs hook:deploy:start:before if declared.
func runBeforeHook(ctx context.Context, d HookRunner, project store.Project, dep store.Deployment, cf *cobaltfile.Cobaltfile, env map[string]string, stdout, stderr io.Writer) error {
	return runHook(ctx, d, project, dep, cf, cobaltfile.HookDeployStartBefore, env, stdout, stderr)
}

// runAfterHook runs hook:deploy:start:after if declared.
func runAfterHook(ctx context.Context, d HookRunner, project store.Project, dep store.Deployment, cf *cobaltfile.Cobaltfile, env map[string]string, stdout, stderr io.Writer) error {
	return runHook(ctx, d, project, dep, cf, cobaltfile.HookDeployStartAfter, env, stdout, stderr)
}
