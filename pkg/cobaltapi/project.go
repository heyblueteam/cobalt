package cobaltapi

// Project is the public shape of a project row. Identity (`id`) is
// stable; display (`name`) is mutable via rename. See
// docs/architecture.md for the identity-vs-display rule.
type Project struct {
	ID                      int64  `json:"id"`
	Name                    string `json:"name"`
	GithubRepo              string `json:"githubRepo"`
	Branch                  string `json:"branch"`
	GithubAppInstallationID *int64 `json:"githubAppInstallationId,omitempty"`
	CreatedAt               int64  `json:"createdAt"`
	UpdatedAt               int64  `json:"updatedAt"`
}

// ProjectCreateRequest is the body of POST /api/projects.
type ProjectCreateRequest struct {
	Name       string `json:"name"`
	GithubRepo string `json:"githubRepo"`
	Branch     string `json:"branch"`
	// Domain, when non-empty, is added to the project as the first
	// domain on creation. Quality-of-life shortcut for the common case;
	// callers can always add domains separately later.
	Domain string `json:"domain,omitempty"`
}

// ProjectRenameRequest is the body of PATCH /api/projects/{name}.
type ProjectRenameRequest struct {
	Name string `json:"name"`
}
