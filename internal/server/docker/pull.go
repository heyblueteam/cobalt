package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// RegistryAuth is one registry login used for a single pull.
type RegistryAuth struct {
	Registry string
	Username string
	Password string
}

// RegistryHost returns the registry host of an image reference, using
// Docker's rule: the first path component is a host only when it contains
// "." or ":" or is "localhost". Everything else is Docker Hub.
func RegistryHost(ref string) string {
	first, _, found := strings.Cut(ref, "/")
	if found && (strings.ContainsAny(first, ".:") || first == "localhost") {
		return first
	}
	return "docker.io"
}

// PullImage pulls ref. With auth, the docker CLI reads a temporary config
// that holds only that registry login, so credentials never enter the
// daemon's own Docker config and are removed when the pull ends.
func (c *Client) PullImage(ctx context.Context, ref string, auth *RegistryAuth, out io.Writer) error {
	if ref == "" {
		return fmt.Errorf("docker.PullImage: empty ref")
	}
	var env map[string]string
	if auth != nil {
		dir, err := os.MkdirTemp("", "cobalt-pull-")
		if err != nil {
			return fmt.Errorf("docker.PullImage: temp config: %w", err)
		}
		defer os.RemoveAll(dir)
		token := base64.StdEncoding.EncodeToString([]byte(auth.Username + ":" + auth.Password))
		cfg, err := json.Marshal(map[string]any{
			"auths": map[string]any{auth.Registry: map[string]string{"auth": token}},
		})
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "config.json"), cfg, 0o600); err != nil {
			return fmt.Errorf("docker.PullImage: write config: %w", err)
		}
		env = map[string]string{"DOCKER_CONFIG": dir}
	}
	if err := c.runner.RunWithEnv(ctx, env, []string{"pull", "--quiet", ref}, nil, out, out); err != nil {
		return fmt.Errorf("docker.PullImage %s: %w", ref, err)
	}
	return nil
}

// TagImage adds dst as a local name for the image src names.
func (c *Client) TagImage(ctx context.Context, src, dst string) error {
	if err := c.run(ctx, "image", "tag", src, dst); err != nil {
		return fmt.Errorf("docker.TagImage %s %s: %w", src, dst, err)
	}
	return nil
}

// ImageLabel returns one label of a local image ("" when unset).
func (c *Client) ImageLabel(ctx context.Context, ref, key string) (string, error) {
	out, err := c.output(ctx, "image", "inspect", "--format", "{{json .Config.Labels}}", ref)
	if err != nil {
		return "", fmt.Errorf("docker.ImageLabel %s: %w", ref, err)
	}
	var labels map[string]string
	if err := json.Unmarshal(out, &labels); err != nil {
		return "", fmt.Errorf("docker.ImageLabel %s: %w", ref, err)
	}
	return labels[key], nil
}
