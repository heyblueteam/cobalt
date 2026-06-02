package caddy

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
)

// DeploymentHeader is the response header ServeService stamps on every
// proxied response, carrying the deployment number the handler was last
// PATCHed to serve. A data-plane probe (deploy.probeDataPlane) reads it back
// through Caddy's own listener to learn which deployment the *running* router
// actually routes to — which can lag the admin config tree under reload
// pressure (the silent divergence behind the post-cutover 502 incident).
const DeploymentHeader = "X-Cobalt-Deployment"

// ServeService swaps a project's reverse-proxy upstream to point at a newly
// started container. This is the atomic cutover point of a deploy: a single
// PATCH against the project's handler @id flips traffic from the old
// container to the new one.
//
// containerName + port form the docker-internal address Caddy will dial,
// e.g. "myapp-7-web" + 3000 → "myapp-7-web:3000".
//
// deploymentNumber is stamped into the DeploymentHeader on proxied responses
// so the running router's identity is observable from the data plane. It
// travels in the *same* handler config as the upstream, so a lagging compiled
// handler that still dials the old container also still emits the old number —
// that mismatch is the divergence signal the reconciler self-heals on.
func (c *Client) ServeService(ctx context.Context, projectID int64, containerName string, port, deploymentNumber int) error {
	body := map[string]any{
		"@id":     ProjectHandlerID(projectID),
		"handler": "reverse_proxy",
		// header_down: set on the response Caddy returns from this upstream.
		// Must be re-sent on every PATCH — the PATCH replaces the whole
		// handler, so omitting it would drop the header.
		"headers": map[string]any{
			"response": map[string]any{
				"set": map[string]any{
					DeploymentHeader: []any{strconv.Itoa(deploymentNumber)},
				},
			},
		},
		"upstreams": []any{
			map[string]any{"dial": containerName + ":" + strconv.Itoa(port)},
		},
	}
	return c.do(ctx, http.MethodPatch, "/id/"+ProjectHandlerID(projectID), body, nil)
}

// ServeStaticSite swaps a project's handler from reverse_proxy to file_server,
// pointed at the directory holding a generated static deployment.
//
// projectName is included only in the on-disk path — every other internal
// reference is by id. Renaming a project does not invalidate this path until
// the next deploy regenerates it; that's by design.
func (c *Client) ServeStaticSite(ctx context.Context, projectID int64, projectName string, deploymentNumber int) error {
	body := map[string]any{
		"@id":     ProjectHandlerID(projectID),
		"handler": "file_server",
		"root":    c.StaticSiteDeploymentPath(projectName, deploymentNumber),
	}
	return c.do(ctx, http.MethodPatch, "/id/"+ProjectHandlerID(projectID), body, nil)
}

// StaticSiteDeploymentPath is the on-disk directory Caddy serves a static
// deployment from, honoring c.StaticSitesDir. Generators write to this
// path; ServeStaticSite makes Caddy read from it.
func (c *Client) StaticSiteDeploymentPath(projectName string, deploymentNumber int) string {
	root := c.StaticSitesDir
	if root == "" {
		root = DefaultStaticSitesDir
	}
	return filepath.Join(root, projectName, "deployments", strconv.Itoa(deploymentNumber))
}

// CurrentUpstream returns the host portion of the project's current
// reverse_proxy upstream, e.g. "myapp-7-web". Returns "" if the project has
// no route, no handler, or is currently serving a static site.
func (c *Client) CurrentUpstream(ctx context.Context, projectID int64) (string, error) {
	var dial string
	err := c.do(ctx, http.MethodGet, "/id/"+ProjectHandlerID(projectID)+"/upstreams/0/dial", nil, &dial)
	if err != nil {
		if IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	if i := indexByte(dial, ':'); i >= 0 {
		return dial[:i], nil
	}
	return dial, nil
}

// StaticSiteDeploymentPath is the package-level form of the per-Client
// helper, using DefaultStaticSitesDir. Prefer Client.StaticSiteDeploymentPath
// when you have a Client handy — it honors per-Client overrides.
func StaticSiteDeploymentPath(projectName string, deploymentNumber int) string {
	return filepath.Join(DefaultStaticSitesDir, projectName, "deployments", strconv.Itoa(deploymentNumber))
}

// indexByte is a tiny helper kept here to avoid pulling in strings just for
// IndexByte; both are equivalent for ASCII.
func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
