package validator

import "regexp"

var (
	ProjectNameRX = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

	EnvKeyRX = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

	DomainRX = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*$`)

	GitHubRepoRX = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*\/[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

	CommitRX = regexp.MustCompile(`^[a-f0-9]{7,40}$`)
)
