package caddy

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
)

// ServeService swaps a project's reverse-proxy upstream to point at a newly
// started container. This is the atomic cutover point of a deploy: a single
// PATCH against the project's handler @id flips traffic from the old
// container to the new one.
//
// containerName + port form the docker-internal address Caddy will dial,
// e.g. "myapp-7-web" + 3000 → "myapp-7-web:3000".
func (c *Client) ServeService(ctx context.Context, projectID int64, containerName string, port int) error {
	body := map[string]any{
		"@id":     ProjectHandlerID(projectID),
		"handler": "reverse_proxy",
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
		"root":    StaticSiteDeploymentPath(projectName, deploymentNumber),
	}
	return c.do(ctx, http.MethodPatch, "/id/"+ProjectHandlerID(projectID), body, nil)
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

// StaticSiteDeploymentPath is the on-disk directory Caddy serves a static
// deployment from. Exposed so the build flow can write files there before
// asking Caddy to switch handlers.
func StaticSiteDeploymentPath(projectName string, deploymentNumber int) string {
	return filepath.Join("/cobalt/srv", projectName, "deployments", strconv.Itoa(deploymentNumber))
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
