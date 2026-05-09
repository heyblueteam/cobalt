package client

import (
	"context"
	"fmt"
	"net/http"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func (c *Client) GetMetaInfo(ctx context.Context) (*cobaltapi.MetaInfo, error) {
	var info cobaltapi.MetaInfo
	if err := c.get(ctx, "/api/meta/info", &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func (c *Client) SetMetaHost(ctx context.Context, host string) (*cobaltapi.MetaInfo, error) {
	var info cobaltapi.MetaInfo
	if err := c.post(ctx, "/api/meta/host", cobaltapi.MetaHostRequest{Host: host}, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

type UpgradeDaemonRequest struct {
	Image string `json:"image,omitempty"`
}

func (c *Client) UpgradeDaemon(ctx context.Context, req UpgradeDaemonRequest) error {
	return c.post(ctx, "/api/meta/upgrade", req, nil)
}

// CreateUpgrade triggers a self-upgrade on the daemon. Returns the
// upgrade row's metadata (id + initial status); follow the actual
// progress via FollowUpgradeOutput.
func (c *Client) CreateUpgrade(ctx context.Context, req cobaltapi.ServerUpgradeRequest) (*cobaltapi.ServerUpgrade, error) {
	var u cobaltapi.ServerUpgrade
	if err := c.post(ctx, "/api/server/upgrade", req, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// GetUpgrade fetches the current state of an upgrade row. Used
// post-stream to read the terminal status (which the SSE close
// doesn't carry — only the bytes of the log do).
func (c *Client) GetUpgrade(ctx context.Context, id string) (*cobaltapi.ServerUpgrade, error) {
	var u cobaltapi.ServerUpgrade
	if err := c.get(ctx, fmt.Sprintf("/api/server/upgrade/%s", id), &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// UpgradeOutput opens the SSE stream for an upgrade's helper log.
func (c *Client) UpgradeOutput(ctx context.Context, id string) (*http.Response, error) {
	return c.StreamGet(ctx, fmt.Sprintf("/api/server/upgrade/%s/output", id))
}

func (c *Client) ListVolumes(ctx context.Context, project string) ([]cobaltapi.Volume, error) {
	var vols []cobaltapi.Volume
	if err := c.get(ctx, fmt.Sprintf("/api/projects/%s/volumes", project), &vols); err != nil {
		return nil, err
	}
	return vols, nil
}

func (c *Client) ExportVolume(ctx context.Context, project, volume string) (*http.Response, error) {
	path := fmt.Sprintf("/api/projects/%s/volumes/%s/export", project, volume)
	return c.PostRaw(ctx, path, "", nil)
}

func (c *Client) ImportVolume(ctx context.Context, project, volume string, data []byte) (*http.Response, error) {
	path := fmt.Sprintf("/api/projects/%s/volumes/%s/import", project, volume)
	return c.PostBytes(ctx, path, data)
}
