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
