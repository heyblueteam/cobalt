package docker

import (
	"fmt"
	"strconv"
)

// Label keys cobalt attaches to every docker resource it creates.
const (
	LabelProjectID        = "cobalt.project.id"
	LabelProjectName      = "cobalt.project.name"
	LabelServiceName      = "cobalt.service.name"
	LabelDeploymentNumber = "cobalt.deployment.number"
)

// projectLabels returns the {key=value, ...} slice in the form docker
// expects for repeated --label flags.
func projectLabels(projectID int64, projectName string) []string {
	return []string{
		LabelProjectID + "=" + strconv.FormatInt(projectID, 10),
		LabelProjectName + "=" + projectName,
	}
}

// serviceLabels extends projectLabels with service-level metadata.
func serviceLabels(projectID int64, projectName, serviceName string, deploymentNumber int) []string {
	return append(
		projectLabels(projectID, projectName),
		LabelServiceName+"="+serviceName,
		LabelDeploymentNumber+"="+strconv.Itoa(deploymentNumber),
	)
}

// FilterByProjectID returns the values for `--filter label=...` that select
// only resources tagged with the given project id. Always one element;
// returned as a slice so callers can splice it into a `--filter X --filter Y`
// argv uniformly with the multi-label variant below.
func FilterByProjectID(projectID int64) []string {
	return []string{fmt.Sprintf("label=%s=%d", LabelProjectID, projectID)}
}

// FilterByDeployment returns the filters that select resources for a single
// deployment of a project. Each element is one `--filter` value; callers
// must emit them as separate `--filter` flags. Docker CLI does NOT accept
// a comma-joined `label=X,label=Y` form — only repeated `--filter` flags
// AND together as expected.
func FilterByDeployment(projectID int64, deploymentNumber int) []string {
	return []string{
		fmt.Sprintf("label=%s=%d", LabelProjectID, projectID),
		fmt.Sprintf("label=%s=%d", LabelDeploymentNumber, deploymentNumber),
	}
}

// withFilterFlags pairs each filter value with `--filter` and appends the
// flag pairs to args. Returns the new args slice.
func withFilterFlags(args []string, filters []string) []string {
	for _, f := range filters {
		args = append(args, "--filter", f)
	}
	return args
}
