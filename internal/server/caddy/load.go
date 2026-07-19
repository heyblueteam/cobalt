package caddy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// applyCobaltRoutes performs a read-modify-write of the `cobalt` HTTP
// server's routes slice: GET the full live config from /config/, hand the
// slice to mutate, and POST the merged document back via /load — Caddy's
// atomic full-config swap (no-op when unchanged, rolled back on a failed
// apply). Unlike the positional `PUT .../routes/0` this replaces, the load
// can't half-apply and re-running it converges instead of duplicating.
//
// Only the objects on the path down to the routes slice are decoded; every
// sibling — the admin and logging blocks, other apps, and any route mutate
// leaves in place (the `cobalt-redirect-*` routes owned by the API domain
// handlers, the daemon host route) — is carried as raw bytes and survives
// the round-trip byte-for-byte. This is deliberately NOT a from-scratch
// serialization of desired state: blocks cobalt doesn't own are never
// rebuilt, so they can never be clobbered.
//
// adminMu is held across the whole read-modify-write so no other cobalt
// writer can interleave between the GET and the /load.
func (c *Client) applyCobaltRoutes(ctx context.Context, mutate func(routes []json.RawMessage) ([]json.RawMessage, error)) error {
	c.adminMu.Lock()
	defer c.adminMu.Unlock()

	var full map[string]json.RawMessage
	if err := c.doLocked(ctx, http.MethodGet, "/config/", nil, &full); err != nil {
		return fmt.Errorf("caddy: read full config: %w", err)
	}
	apps, err := rawObject(full, "apps")
	if err != nil {
		return err
	}
	httpApp, err := rawObject(apps, "http")
	if err != nil {
		return err
	}
	servers, err := rawObject(httpApp, "servers")
	if err != nil {
		return err
	}
	cobaltSrv, err := rawObject(servers, "cobalt")
	if err != nil {
		return err
	}
	var routes []json.RawMessage
	if raw, ok := cobaltSrv["routes"]; ok {
		if err := json.Unmarshal(raw, &routes); err != nil {
			return fmt.Errorf("caddy: decode cobalt routes: %w", err)
		}
	}

	mutated, err := mutate(routes)
	if err != nil {
		return err
	}

	// Re-encode only the objects we opened on the way down; their sibling
	// keys are still raw bytes and marshal back unchanged.
	for _, step := range []struct {
		parent map[string]json.RawMessage
		key    string
		child  any
	}{
		{cobaltSrv, "routes", mutated},
		{servers, "cobalt", cobaltSrv},
		{httpApp, "servers", servers},
		{apps, "http", httpApp},
		{full, "apps", apps},
	} {
		raw, err := json.Marshal(step.child)
		if err != nil {
			return fmt.Errorf("caddy: re-encode %q: %w", step.key, err)
		}
		step.parent[step.key] = raw
	}

	if err := c.doLocked(ctx, http.MethodPost, "/load", full, nil); err != nil {
		return fmt.Errorf("caddy: load full config: %w", err)
	}
	return nil
}

// rawObject decodes obj[key] as a JSON object whose values stay raw. A
// missing key or non-object value is an error: the bootstrap config
// (WriteInitConfig) always writes the blocks on the path to the cobalt
// server, so their absence means this Caddy isn't running a cobalt config —
// failing loudly beats inventing structure over live state we don't own.
func rawObject(obj map[string]json.RawMessage, key string) (map[string]json.RawMessage, error) {
	raw, ok := obj[key]
	if !ok {
		return nil, fmt.Errorf("caddy: full config has no %q block", key)
	}
	var child map[string]json.RawMessage
	if err := json.Unmarshal(raw, &child); err != nil {
		return nil, fmt.Errorf("caddy: decode %q block: %w", key, err)
	}
	return child, nil
}

// routeID returns the @id of a raw route object, or "" when it has none.
func routeID(raw json.RawMessage) string {
	var doc struct {
		ID string `json:"@id"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	return doc.ID
}
