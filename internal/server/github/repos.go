package github

import (
	"context"
	"fmt"
	"net/http"
)

// Repository is the subset of GitHub's repository object we persist.
type Repository struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
}

// ListInstallationRepos returns every repo the installation has access to.
// Pagination is handled internally — for our scale (Blue has ~10 repos)
// this is one round trip, but we walk the Link header in case of growth.
//
// installationToken is the per-installation access token from
// MintInstallationToken.
func (c *Client) ListInstallationRepos(ctx context.Context, installationToken string) ([]Repository, error) {
	const perPage = 100
	var all []Repository
	for page := 1; ; page++ {
		path := fmt.Sprintf("/installation/repositories?per_page=%d&page=%d", perPage, page)
		var resp struct {
			TotalCount   int          `json:"total_count"`
			Repositories []Repository `json:"repositories"`
		}
		if err := c.do(ctx, http.MethodGet, path, authToken, installationToken, nil, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Repositories...)
		if len(resp.Repositories) < perPage || len(all) >= resp.TotalCount {
			break
		}
	}
	return all, nil
}

// CloneURL returns the HTTPS URL to git-clone a repo using an installation
// access token. The token is embedded in the URL using GitHub's
// `x-access-token` placeholder username.
//
// CAREFUL: never log the result. The token is in the URL.
func CloneURL(installationToken, fullName string) string {
	return fmt.Sprintf("https://x-access-token:%s@github.com/%s.git", installationToken, fullName)
}

// AnonymousCloneURL returns the unauthenticated HTTPS clone URL for a public
// repo. Used when no GitHub App installation grants us access — the deploy
// will succeed iff the repo is public.
func AnonymousCloneURL(fullName string) string {
	return fmt.Sprintf("https://github.com/%s.git", fullName)
}
