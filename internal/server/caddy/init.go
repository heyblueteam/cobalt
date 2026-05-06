package caddy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// InitConfigPath is where the daemon writes Caddy's bootstrap config. Caddy
// reads this on startup, opens its admin API on a unix socket, and from
// there cobalt drives all subsequent config changes via the admin API.
const InitConfigPath = "/initconfig/config.json"

// WriteInitConfig writes the canonical bootstrap config Caddy reads at
// startup. The file pins the admin API to a unix socket (so only the cobalt
// daemon — not deployed services — can read or write it) and installs a
// minimal route that forwards the daemon's own hostname back into the daemon
// container.
//
// behindTunnel toggles between :80 (when fronted by something terminating
// TLS, like Cloudflare Tunnel) and :443 (the standalone case where Caddy
// terminates TLS itself).
func WriteInitConfig(path, daemonHost string, behindTunnel bool) error {
	listen := ":443"
	if behindTunnel {
		listen = ":80"
	}

	cfg := map[string]any{
		"admin": map[string]any{
			"enforce_origin": false,
			"listen":         "unix//cobalt/caddy-socket/caddy.sock",
			"origins":        []any{"cobalt-caddy"},
		},
		"apps": map[string]any{
			"http": map[string]any{
				"servers": map[string]any{
					"cobalt": map[string]any{
						"listen":    []any{listen},
						"protocols": []any{"h1", "h2"},
						"logs":      map[string]any{},
						"routes": []any{
							map[string]any{
								"@id": daemonHostID,
								"handle": []any{
									map[string]any{
										"handler": "subroute",
										"routes": []any{
											map[string]any{
												"handle": []any{
													map[string]any{
														"handler": "encode",
														"encodings": map[string]any{
															"gzip": map[string]any{},
															"zstd": map[string]any{},
														},
													},
													map[string]any{
														"@id":     daemonHandlerID,
														"handler": "reverse_proxy",
														"upstreams": []any{
															map[string]any{"dial": "cobalt:80"},
														},
													},
												},
											},
										},
									},
								},
								"match":    []any{map[string]any{"host": []any{daemonHost}}},
								"terminal": true,
							},
						},
					},
				},
			},
		},
		"logging": map[string]any{
			"logs": map[string]any{
				"default": map[string]any{
					"encoder": map[string]any{
						"format": "filter",
						"wrap":   map[string]any{"format": "json"},
						"fields": map[string]any{
							// Strip noisy / PII-adjacent fields out of access logs.
							"request>headers": map[string]any{"filter": "delete"},
							"request>tls":     map[string]any{"filter": "delete"},
							"resp_headers":    map[string]any{"filter": "delete"},
							"user_id":         map[string]any{"filter": "delete"},
						},
					},
				},
			},
		},
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("caddy: write init config: mkdir: %w", err)
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("caddy: write init config: marshal: %w", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("caddy: write init config: %w", err)
	}
	return nil
}
