package client

import (
	"context"
	"fmt"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func (c *Client) GetScale(ctx context.Context, project string) (*cobaltapi.ScaleInfo, error) {
	var info cobaltapi.ScaleInfo
	if err := c.get(ctx, fmt.Sprintf("/api/projects/%s/scale", project), &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func (c *Client) SetScale(ctx context.Context, project string, req cobaltapi.ScaleSetRequest) (*cobaltapi.ScaleInfo, error) {
	var info cobaltapi.ScaleInfo
	if err := c.post(ctx, fmt.Sprintf("/api/projects/%s/scale", project), req, &info); err != nil {
		return nil, err
	}
	return &info, nil
}
