// Package cliconfig reads and writes the cobalt CLI's local config at
// ~/.cobalt/config.json. The file holds one entry per cobalt server the
// user is connected to, including the API key and the optional default
// project for that server.
package cliconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the on-disk shape of ~/.cobalt/config.json.
type Config struct {
	Servers       map[string]Server `json:"servers"`
	DefaultServer string            `json:"defaultServer,omitempty"`
}

// Server is one cobalt daemon the CLI is registered against.
type Server struct {
	Host           string `json:"host"`
	APIKey         string `json:"apiKey"`
	CurrentProject string `json:"currentProject,omitempty"`
	// CACertPEM is a PEM-encoded CA certificate to trust when verifying
	// the daemon's TLS chain, in addition to the system trust store. Set
	// at init time for `--insecure-tls` daemons (Caddy's local CA isn't
	// in the system pool); empty for daemons with a publicly-trusted
	// cert. Pinning the operator-installed CA here means we keep real
	// cert verification — no MITM hole — without forcing the operator
	// to install the cert globally.
	CACertPEM string `json:"caCertPEM,omitempty"`
}

// DefaultPath returns the canonical ~/.cobalt/config.json path. It does not
// require the file to exist.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cliconfig: home dir: %w", err)
	}
	return filepath.Join(home, ".cobalt", "config.json"), nil
}

// Load reads path and returns a Config. If path does not exist, Load returns
// an empty Config (not an error) — first-run callers can mutate and Save.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{Servers: map[string]Server{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cliconfig: read %s: %w", path, err)
	}
	cfg := &Config{}
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("cliconfig: parse %s: %w", path, err)
	}
	if cfg.Servers == nil {
		cfg.Servers = map[string]Server{}
	}
	return cfg, nil
}

// Save writes cfg to path atomically (write-temp + rename) with 0o600 perms,
// since it contains API keys.
func Save(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("cliconfig: mkdir: %w", err)
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("cliconfig: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return fmt.Errorf("cliconfig: tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("cliconfig: write: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("cliconfig: chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("cliconfig: close: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("cliconfig: rename: %w", err)
	}
	return nil
}

// Active returns the server the CLI should talk to: the explicit name if
// given, otherwise the configured default. Returns an error if no server can
// be resolved.
func (c *Config) Active(explicit string) (string, Server, error) {
	if explicit != "" {
		s, ok := c.Servers[explicit]
		if !ok {
			return "", Server{}, fmt.Errorf("cliconfig: unknown server %q", explicit)
		}
		return explicit, s, nil
	}
	if c.DefaultServer == "" {
		return "", Server{}, errors.New("cliconfig: no default server set; use --server or run 'cobalt init'")
	}
	s, ok := c.Servers[c.DefaultServer]
	if !ok {
		return "", Server{}, fmt.Errorf("cliconfig: default server %q not in servers list", c.DefaultServer)
	}
	return c.DefaultServer, s, nil
}

// SetCurrentProject updates server.CurrentProject for a given server name.
func (c *Config) SetCurrentProject(server, project string) error {
	s, ok := c.Servers[server]
	if !ok {
		return fmt.Errorf("cliconfig: unknown server %q", server)
	}
	s.CurrentProject = project
	c.Servers[server] = s
	return nil
}
