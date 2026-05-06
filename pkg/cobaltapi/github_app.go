package cobaltapi

// GithubApp is the public shape of a registered GitHub App. Sensitive
// fields (private_key, webhook_secret, client_secret) are intentionally
// omitted — they're only ever exposed at creation time and never round-
// tripped through the API.
type GithubApp struct {
	ID        int64  `json:"id"`
	AppID     int64  `json:"appId"`
	Slug      string `json:"slug,omitempty"`
	Name      string `json:"name,omitempty"`
	Owner     string `json:"owner"`
	HTMLURL   string `json:"htmlUrl,omitempty"`
	CreatedAt int64  `json:"createdAt"`
}

// GithubAppInstallation is a single org/user installation of an App.
type GithubAppInstallation struct {
	ID             int64  `json:"id"`
	AppID          int64  `json:"appId"` // local cobalt id
	InstallationID int64  `json:"installationId"`
	AccountLogin   string `json:"accountLogin"`
	CreatedAt      int64  `json:"createdAt"`
}

// GithubAppRepo is a repo accessible to one of cobalt's installations.
type GithubAppRepo struct {
	ID             int64  `json:"id"`
	InstallationID int64  `json:"installationId"`
	RepoID         int64  `json:"repoId"`
	FullName       string `json:"fullName"`
	Private        bool   `json:"private"`
	DefaultBranch  string `json:"defaultBranch,omitempty"`
}

// PendingAppCreateRequest is the body of POST /api/github-apps/create.
type PendingAppCreateRequest struct {
	// Organization is the GitHub org the App will be created under. Use
	// the empty string to create a user-scoped App against the user's
	// own account (rare for our setup; typically set the org).
	Organization string `json:"organization"`
}

// PendingAppCreateResponse is the response shape of the pending-app
// creation step. The CLI takes URL and opens it in the user's browser.
type PendingAppCreateResponse struct {
	ID        int64  `json:"id"`
	URL       string `json:"url"`
	ExpiresAt int64  `json:"expiresAt"`
}

// PruneResponse summarizes a `cobalt github apps prune` invocation.
type PruneResponse struct {
	AppsRemoved          int `json:"appsRemoved"`
	InstallationsRemoved int `json:"installationsRemoved"`
	ReposAdded           int `json:"reposAdded"`
	ReposRemoved         int `json:"reposRemoved"`
}
