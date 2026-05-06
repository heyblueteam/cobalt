package docker

import (
	"fmt"
	"strings"
)

// ServiceName is the docker swarm service name for a project's service at
// a given deployment number. Matches upstream's scheme so operators reading
// `docker service ls` see something familiar.
//
//	{projectName}-{deploymentNumber}-{serviceName}
func ServiceName(projectName string, deploymentNumber int, serviceName string) string {
	return fmt.Sprintf("%s-%d-%s", projectName, deploymentNumber, serviceName)
}

// NetworkName is the docker overlay network for a project's deployment.
//
//	cobalt-project-{projectName}-{deploymentNumber}
func NetworkName(projectName string, deploymentNumber int) string {
	return fmt.Sprintf("cobalt-project-%s-%d", projectName, deploymentNumber)
}

// HookContainerName is the one-shot container name for a deploy hook.
// hook is the cobaltfile hook key, e.g. "hook:deploy:start:before"; we
// flatten the colons so docker is happy with it.
func HookContainerName(projectName, hook string, deploymentNumber int) string {
	flat := strings.ReplaceAll(hook, ":", "-")
	return fmt.Sprintf("%s-%s.%d", projectName, flat, deploymentNumber)
}

// RunContainerName is the container name for a `cobalt run` invocation.
// runNumber is monotonic per project (sourced from the command_runs table).
func RunContainerName(projectName string, runNumber int64) string {
	return fmt.Sprintf("%s-run.%d", projectName, runNumber)
}

// InternalImageName is the tag cobalt builds and pushes for a project
// service's image at a given deployment number.
//
//	cobalt/project-{projectName}-{imageName}:{deploymentNumber}
func InternalImageName(projectName, imageName string, deploymentNumber int) string {
	return fmt.Sprintf("cobalt/project-%s-%s:%d", projectName, imageName, deploymentNumber)
}

// InternalImagePrefix is the tag prefix matching all internal images for a
// project. Used by image cleanup to filter `docker image ls` output.
func InternalImagePrefix(projectName string) string {
	return fmt.Sprintf("cobalt/project-%s-", projectName)
}
