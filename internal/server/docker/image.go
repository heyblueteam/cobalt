package docker

import (
	"context"
	"strconv"
	"strings"
)

// ImageInfo identifies a single tagged cobalt-internal image.
type ImageInfo struct {
	Tag              string // full e.g. "cobalt/project-myapp-web:42"
	Repository       string // "cobalt/project-myapp-web"
	DeploymentNumber int    // 42
	ImageID          string
}

// ListInternalImages returns every internal image for a project, parsed
// into structured form. Repositories that don't end in a numeric tag are
// skipped — they aren't cobalt-managed.
func (c *Client) ListInternalImages(ctx context.Context, projectName string) ([]ImageInfo, error) {
	prefix := InternalImagePrefix(projectName)
	out, err := c.output(ctx,
		"image", "ls",
		"--filter", "reference="+prefix+"*",
		"--format", "{{.Repository}}:{{.Tag}}\t{{.ID}}",
	)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	var images []ImageInfo
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) != 2 {
			continue
		}
		full := fields[0]
		id := fields[1]
		colon := strings.LastIndexByte(full, ':')
		if colon <= 0 || colon == len(full)-1 {
			continue
		}
		repo := full[:colon]
		tag := full[colon+1:]
		num, err := strconv.Atoi(tag)
		if err != nil {
			continue
		}
		images = append(images, ImageInfo{
			Tag:              full,
			Repository:       repo,
			DeploymentNumber: num,
			ImageID:          id,
		})
	}
	return images, nil
}

// RemoveImage deletes an image by tag. Treats "no such image" as success.
func (c *Client) RemoveImage(ctx context.Context, tag string) error {
	if err := c.run(ctx, "image", "rm", tag); err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}
	return nil
}

// ImageExists returns true iff the given tag is present locally.
// Used by the rollback flow to refuse pre-flight when the cached
// image for a target deployment has been pruned.
func (c *Client) ImageExists(ctx context.Context, tag string) (bool, error) {
	out, err := c.output(ctx, "image", "ls", "--format", "{{.Repository}}:{{.Tag}}", tag)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == tag {
			return true, nil
		}
	}
	return false, nil
}
