package github

import (
	"context"
	"fmt"
	"net/http"
)

// ConvertedApp is the response shape of GitHub's manifest-conversion
// endpoint. It contains everything we need to persist a new App.
type ConvertedApp struct {
	ID            int64               `json:"id"`
	Slug          string              `json:"slug"`
	Name          string              `json:"name"`
	HTMLURL       string              `json:"html_url"`
	Owner         ConvertedAppOwner   `json:"owner"`
	WebhookSecret string              `json:"webhook_secret"`
	PEM           string              `json:"pem"`            // RSA private key
	ClientID      string              `json:"client_id"`
	ClientSecret  string              `json:"client_secret"`
	Permissions   map[string]string   `json:"permissions"`
	Events        []string            `json:"events"`
}

// ConvertedAppOwner is the owner block in a manifest-conversion response.
type ConvertedAppOwner struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
	Type  string `json:"type"` // "Organization" or "User"
}

// ConvertManifestCode posts the temporary `code` GitHub returned to our
// callback URL, exchanging it for the new App's full credentials.
//
// This is the one endpoint we call without auth — the `code` itself is the
// proof the user just completed the manifest flow.
//
// Reference: https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest#3-you-exchange-the-temporary-code-to-retrieve-the-app-configuration
func (c *Client) ConvertManifestCode(ctx context.Context, code string) (*ConvertedApp, error) {
	path := fmt.Sprintf("/app-manifests/%s/conversions", code)
	var out ConvertedApp
	if err := c.do(ctx, http.MethodPost, path, authNone, "", nil, &out); err != nil {
		return nil, fmt.Errorf("github: convert manifest code: %w", err)
	}
	return &out, nil
}

// InstallationsURL returns the GitHub URL the daemon redirects users to
// after a successful App registration so they can install the App on a
// repo. ownerType is "Organization" or "User" from ConvertedApp.Owner.Type.
func InstallationsURL(htmlURL string, ownerID int64, ownerType string) string {
	if ownerType == "Organization" {
		return fmt.Sprintf("%s/installations/new/permissions?target_id=%d", htmlURL, ownerID)
	}
	return fmt.Sprintf("%s/installations/new", htmlURL)
}

// Manifest is the JSON document we POST to GitHub to start the App-
// registration flow. The user submits this via a form on a GitHub page;
// GitHub then redirects back to our callback URL with a code.
type Manifest struct {
	Name        string              `json:"name"`
	URL         string              `json:"url"`               // App's homepage; we point at the daemon's host
	HookAttrs   ManifestHookAttrs   `json:"hook_attributes"`
	RedirectURL string              `json:"redirect_url"`      // where GitHub redirects after creation
	Public      bool                `json:"public"`            // we always set false
	Events      []string            `json:"default_events"`    // ["push"]
	Permissions map[string]string   `json:"default_permissions"` // {"contents":"read"}
	SetupURL    string              `json:"setup_url,omitempty"`
}

// ManifestHookAttrs is the webhook configuration GitHub will install.
type ManifestHookAttrs struct {
	URL string `json:"url"`
}

// BuildManifest returns the canonical cobalt manifest, parameterized only
// by the daemon's public host. Callers fill in pendingAppID for
// RedirectURL.
func BuildManifest(daemonHost, name, pendingAppID string) Manifest {
	return Manifest{
		Name:        name,
		URL:         "https://" + daemonHost,
		HookAttrs:   ManifestHookAttrs{URL: "https://" + daemonHost + "/webhooks/github"},
		RedirectURL: "https://" + daemonHost + "/github-apps/" + pendingAppID + "/created",
		Public:      false,
		Events:      []string{"push"},
		Permissions: map[string]string{"contents": "read", "metadata": "read"},
	}
}
