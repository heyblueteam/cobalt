package client

import (
	"context"
	"net/url"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func (c *Client) ListRegistries(ctx context.Context) ([]cobaltapi.RegistryCredential, error) {
	var out []cobaltapi.RegistryCredential
	if err := c.get(ctx, "/api/registries", &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) SetRegistry(ctx context.Context, registry string, req cobaltapi.RegistryCredentialSetRequest) (*cobaltapi.RegistryCredential, error) {
	var out cobaltapi.RegistryCredential
	if err := c.post(ctx, "/api/registries/"+url.PathEscape(registry), req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteRegistry(ctx context.Context, registry string) error {
	return c.del(ctx, "/api/registries/"+url.PathEscape(registry))
}
