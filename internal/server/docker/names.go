package docker

import (
	"fmt"
	"strconv"
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

// StablePublicWebServiceName is the durable Swarm service name for a
// project's publicly-routed web process. It is deliberately keyed by the
// immutable project ID rather than its display name or deployment number, so
// Caddy never holds a DNS name that disappears during generation cleanup.
func StablePublicWebServiceName(projectID int64) string {
	return fmt.Sprintf("cobalt-web-%d", projectID)
}

// WebGeneration parses the deployment number out of a project's `web` service
// name — the inverse of ServiceName(projectName, n, "web"). It returns
// ok=false for names that don't belong to this project or aren't the `web`
// service (worker/cron services, other projects, malformed names).
//
// projectName is taken as known (not parsed back out) so a hyphenated project
// name — or a hyphenated service name — doesn't make the split ambiguous.
func WebGeneration(projectName, serviceName string) (int, bool) {
	return Generation(projectName, "web", serviceName)
}

// Generation parses the deployment number out of a full swarm service name —
// the inverse of ServiceName(projectName, n, svcName) for one known project
// and logical service. ok=false for other projects, other services, and
// malformed names.
//
// Both projectName and svcName are taken as known (not parsed back out), so
// hyphens in either never make the split ambiguous: "haraka-9-old-smtp" is
// NOT a generation of svc "smtp" because the middle segment "9-old" isn't a
// number.
func Generation(projectName, svcName, fullName string) (int, bool) {
	rest, ok := strings.CutPrefix(fullName, projectName+"-")
	if !ok {
		return 0, false
	}
	// rest is "{number}-{svcName}", e.g. "114-web"; split on the first '-'.
	i := strings.IndexByte(rest, '-')
	if i <= 0 {
		return 0, false
	}
	if rest[i+1:] != svcName {
		return 0, false
	}
	n, err := strconv.Atoi(rest[:i])
	if err != nil {
		return 0, false
	}
	return n, true
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

// CronContainerName is the per-fire one-shot container name for a
// cron service. The epoch suffix prevents collisions when a slow
// run overlaps the next fire's start.
func CronContainerName(projectName, serviceName string, epochNanos int64) string {
	flat := strings.ReplaceAll(serviceName, ":", "-")
	return fmt.Sprintf("cobalt-cron-%s-%s-%d", projectName, flat, epochNanos)
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
