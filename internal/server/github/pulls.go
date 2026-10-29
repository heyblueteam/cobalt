package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// PullRequest is the subset of GitHub's pull request object we render in
// deploy notifications.
type PullRequest struct {
	Number   int        `json:"number"`
	HTMLURL  string     `json:"html_url"`
	Title    string     `json:"title"`
	Merged   bool       `json:"merged"`
	MergedAt *time.Time `json:"merged_at"`
}

// ListPullRequestsForCommit returns the pull requests associated with a
// commit. GitHub returns an empty list for commits pushed directly to a
// branch. The caller picks the merged one.
//
// Requires the GitHub App permission "pull requests: read". Without it the
// API answers 403 and callers should treat the result as "no PR".
func (c *Client) ListPullRequestsForCommit(ctx context.Context, installationToken, fullName, sha string) ([]PullRequest, error) {
	if installationToken == "" {
		return nil, fmt.Errorf("github: list pull requests: empty installation token")
	}
	path := fmt.Sprintf("/repos/%s/commits/%s/pulls", fullName, url.PathEscape(sha))
	var prs []PullRequest
	if err := c.do(ctx, http.MethodGet, path, authToken, installationToken, nil, &prs); err != nil {
		return nil, err
	}
	return prs, nil
}
