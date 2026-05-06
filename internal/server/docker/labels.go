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

// FilterByProjectID returns the value for `--filter label=...` that selects
// only resources tagged with the given project id.
func FilterByProjectID(projectID int64) string {
	return fmt.Sprintf("label=%s=%d", LabelProjectID, projectID)
}

// FilterByDeployment returns the filter that selects resources for a single
// deployment of a project.
func FilterByDeployment(projectID int64, deploymentNumber int) string {
	return fmt.Sprintf(
		"label=%s=%d,label=%s=%d",
		LabelProjectID, projectID,
		LabelDeploymentNumber, deploymentNumber,
	)
}
