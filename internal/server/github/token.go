package github

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// TokenRefreshMargin is how far ahead of a token's expiry we treat it as
// stale and refresh proactively. GitHub access tokens are good for ~1 hour;
// we refresh 5 minutes before that.
const TokenRefreshMargin = 5 * time.Minute

// InstallationToken is a short-lived access token issued for a specific
// installation. The Token authenticates HTTPS git operations and
// installation-scoped API calls.
type InstallationToken struct {
	Token     string
	ExpiresAt time.Time
}

// Valid reports whether the token can still be used at the supplied "now"
// without crossing the refresh margin.
func (t InstallationToken) Valid(now time.Time) bool {
	if t.Token == "" {
		return false
	}
	return now.Add(TokenRefreshMargin).Before(t.ExpiresAt)
}

// MintInstallationToken exchanges an app JWT for a fresh installation
// access token via POST /app/installations/{id}/access_tokens.
//
// Reference: https://docs.github.com/en/rest/apps/apps#create-an-installation-access-token-for-an-app
func (c *Client) MintInstallationToken(ctx context.Context, jwt string, installationID int64) (InstallationToken, error) {
	path := fmt.Sprintf("/app/installations/%d/access_tokens", installationID)
	var resp struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := c.do(ctx, http.MethodPost, path, authJWT, jwt, nil, &resp); err != nil {
		return InstallationToken{}, fmt.Errorf("github: mint token for installation %d: %w", installationID, err)
	}
	return InstallationToken{Token: resp.Token, ExpiresAt: resp.ExpiresAt}, nil
}

// AppExists checks whether the App is still registered on GitHub. Used by
// `prune` to detect "user deleted the app from their org" without waiting
// for a webhook.
//
// Returns nil on success, *HTTPError(404) if the app is gone, other errors
// for transient failures.
func (c *Client) AppExists(ctx context.Context, jwt string) (bool, error) {
	if err := c.do(ctx, http.MethodGet, "/app", authJWT, jwt, nil, nil); err != nil {
		if IsStatus(err, http.StatusNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
