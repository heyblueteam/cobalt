package caddy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// defaultProtocols is the protocol list WriteInitConfig writes for the
// cobalt server; applyCobaltRoutes backfills it on configs that predate it.
const defaultProtocols = `["h1","h2"]`

// defaultGracePeriod bounds how long a superseded Caddy server keeps its
// existing connections after a reload; see WriteInitConfig.
const defaultGracePeriod = `"30s"`

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
	// Backfill the http app's grace_period the same way protocols is
	// backfilled below: hosts bootstrapped before WriteInitConfig wrote it
	// run with Caddy's eternal default, which is what let a Cloudflare
	// keep-alive connection outlive the reaped upstream it was pinned to
	// (2026-07-14, 2026-09-06). Only when absent, so an operator's explicit
	// value survives.
	if _, ok := httpApp["grace_period"]; !ok {
		httpApp["grace_period"] = json.RawMessage(defaultGracePeriod)
	}
	servers, err := rawObject(httpApp, "servers")
	if err != nil {
		return err
	}
	cobaltSrv, err := rawObject(servers, "cobalt")
	if err != nil {
		return err
	}
	// Pin the server to TCP-only HTTP (h1/h2) when no explicit protocol
	// list exists. WriteInitConfig has set this on fresh installs since
	// 2026-05, but hosts bootstrapped before then carry a config without
	// the key, and Caddy 2.7 then defaults to h1+h2+h3 and advertises
	// `Alt-Svc: h3=":443"` on every response. cobalt only publishes TCP
	// 443 on the swarm, so the UDP handshake browsers attempt is dropped
	// silently by the host firewall (2026-09-08 incident). Setting the key
	// only when absent keeps a deliberate operator choice — including a
	// future real HTTP/3 rollout — untouched.
	if _, ok := cobaltSrv["protocols"]; !ok {
		cobaltSrv["protocols"] = json.RawMessage(defaultProtocols)
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
