package cobaltapi

// Project is the public shape of a project row. Identity (`id`) is
// stable; display (`name`) is mutable via rename. See
// docs/architecture.md for the identity-vs-display rule.
type Project struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	GithubRepo string `json:"githubRepo"`
	Branch     string `json:"branch"`
	// Path is an optional sub-path inside the repo where the project's
	// cobalt.json and Dockerfile contexts live. Empty (the default)
	// means the repo root. Set when one repo hosts multiple deployments
	// (monorepo layout, e.g. "api", "services/web").
	Path                    string `json:"path,omitempty"`
	GithubAppInstallationID *int64 `json:"githubAppInstallationId,omitempty"`
	CreatedAt               int64  `json:"createdAt"`
	UpdatedAt               int64  `json:"updatedAt"`
}

// ProjectCreateRequest is the body of POST /api/projects.
type ProjectCreateRequest struct {
	Name       string `json:"name"`
	GithubRepo string `json:"githubRepo"`
	Branch     string `json:"branch"`
	// Path, when non-empty, scopes the deploy to a sub-path inside the
	// repo. The cobalt.json + Dockerfile contexts are resolved relative
	// to `<repo>/<path>`. Use "api" or "services/api" — no leading or
	// trailing slash, no `..`. Empty = repo root.
	Path string `json:"path,omitempty"`
	// Domain, when non-empty, is added to the project as the first
	// domain on creation. Quality-of-life shortcut for the common case;
	// callers can always add domains separately later.
	Domain string `json:"domain,omitempty"`
}

// ProjectRenameRequest is the body of PATCH /api/projects/{name}.
type ProjectRenameRequest struct {
	Name string `json:"name"`
}

// ProjectUpdateSourceRequest is the body of PATCH /api/projects/{name}/source.
// Retargets which GitHub repo, branch, and sub-path the project tracks. All
// three fields are required; the CLI's `cobalt projects update` provides
// partial-update ergonomics by fetching current state and merging user
// flags before sending the full request.
//
// Existing domains, env vars, deployments, and running services are
// unaffected — the next deploy reads from the new source.
type ProjectUpdateSourceRequest struct {
	GithubRepo string `json:"githubRepo"`
	Branch     string `json:"branch"`
	Path       string `json:"path"`
}
