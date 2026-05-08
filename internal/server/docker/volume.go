package docker

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// VolumeName is the per-project naming convention for docker volumes,
// keyed by project id (per the identity/display rule). Volumes survive
// across deployments — they are user data — so they cannot be tied to a
// deployment number.
//
//	cobalt-volume-{projectID}-{name}
func VolumeName(projectID int64, name string) string {
	return fmt.Sprintf("cobalt-volume-%d-%s", projectID, name)
}

// CreateVolume creates a named docker volume tagged with cobalt's project
// labels. Idempotent: returns nil if the volume already exists.
func (c *Client) CreateVolume(ctx context.Context, projectID int64, projectName, volumeName string) error {
	name := VolumeName(projectID, volumeName)
	exists, err := c.VolumeExists(ctx, name)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	args := []string{"volume", "create"}
	for _, l := range projectLabels(projectID, projectName) {
		args = append(args, "--label", l)
	}
	args = append(args, name)
	return c.run(ctx, args...)
}

// VolumeExists reports whether a volume with the given full name is
// already present.
func (c *Client) VolumeExists(ctx context.Context, name string) (bool, error) {
	out, err := c.output(ctx,
		"volume", "ls", "--filter", "name=^"+name+"$", "--format", "{{.Name}}",
	)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == name {
			return true, nil
		}
	}
	return false, nil
}

// ListVolumesForProject returns every cobalt-managed volume for a project.
//
// We filter by name prefix (cobalt-volume-{id}-) rather than by the
// `cobalt.project.id` label because volumes created via the docker
// auto-create-on-mount path (pre-P0-#1) carry no labels but do carry
// the right name. The name format is canonical (see VolumeName) and
// the trailing dash + service name segment guarantees no collisions
// between project ids that share a numeric prefix
// (`cobalt-volume-1-` does not match `cobalt-volume-10-data`).
func (c *Client) ListVolumesForProject(ctx context.Context, projectID int64) ([]string, error) {
	prefix := VolumeName(projectID, "")
	args := []string{"volume", "ls", "--filter", "name=" + prefix, "--format", "{{.Name}}"}
	out, err := c.output(ctx, args...)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	var vols []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			vols = append(vols, line)
		}
	}
	return vols, nil
}

// ExportVolume runs `docker run --rm --volume <vol>:/data busybox tar -C /data -cf - .`
// piping the resulting tar stream to w.
func (c *Client) ExportVolume(ctx context.Context, volumeName string, w io.Writer) error {
	args := []string{
		"run", "--rm",
		"--mount", "type=volume,source=" + volumeName + ",destination=/data",
		"busybox",
		"tar", "-C", "/data", "-cf", "-", ".",
	}
	return c.runner.Run(ctx, args, nil, w, nil)
}

// ImportVolume reads a tar stream from r into the named docker volume.
// The volume must already exist.
func (c *Client) ImportVolume(ctx context.Context, volumeName string, r io.Reader) error {
	args := []string{
		"run", "--rm", "-i",
		"--mount", "type=volume,source=" + volumeName + ",destination=/data",
		"busybox",
		"tar", "-C", "/data", "-xf", "-",
	}
	return c.runner.Run(ctx, args, r, nil, nil)
}
