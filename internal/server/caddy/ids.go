package caddy

import "fmt"

// @id naming convention for project routes. All keyed by the project's stable
// id, not its display name, per docs/architecture.md.
//
// Caddy treats @id as an opaque addressable label; these strings are only
// meaningful to cobalt code.

// ProjectRouteID is the @id of the top-level subroute that owns a project's
// domain matchers and reverse-proxy handler.
func ProjectRouteID(projectID int64) string {
	return fmt.Sprintf("cobalt-project-%d", projectID)
}

// ProjectHostsID is the @id of the host matcher inside a project route.
// Used to update the domain list without rewriting the rest of the route.
func ProjectHostsID(projectID int64) string {
	return fmt.Sprintf("cobalt-project-hosts-%d", projectID)
}

// ProjectHandlerID is the @id of the reverse-proxy handler inside a project
// route. Used by ServeService to swap the upstream during a deploy cutover.
func ProjectHandlerID(projectID int64) string {
	return fmt.Sprintf("cobalt-project-handler-%d", projectID)
}

// RedirectID is the @id of an apex→www (or www→apex) redirect route. Keyed
// by the domains.id row that owns it.
func RedirectID(rowID int64) string {
	return fmt.Sprintf("cobalt-redirect-%d", rowID)
}

// daemonHostID and daemonHandlerID address the route Caddy uses to forward
// requests for the daemon's own hostname back into the daemon container.
const (
	daemonHostID    = "cobalt-daemon-host"
	daemonHandlerID = "cobalt-daemon-handler"
)
