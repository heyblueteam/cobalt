package client

import (
	"context"
	"strconv"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func (c *Client) ListAPIKeys(ctx context.Context) ([]cobaltapi.APIKey, error) {
	var keys []cobaltapi.APIKey
	if err := c.get(ctx, "/api/apikeys", &keys); err != nil {
		return nil, err
	}
	return keys, nil
}

func (c *Client) CreateAPIKey(ctx context.Context, req cobaltapi.APIKeyCreateRequest) (*cobaltapi.APIKeyCreateResponse, error) {
	var resp cobaltapi.APIKeyCreateResponse
	if err := c.post(ctx, "/api/apikeys", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) DeleteAPIKey(ctx context.Context, id int64) error {
	return c.del(ctx, "/api/apikeys/"+strconv.FormatInt(id, 10))
}
