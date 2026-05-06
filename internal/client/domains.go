package client

import (
	"context"
	"fmt"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func (c *Client) ListDomains(ctx context.Context, project string) ([]cobaltapi.Domain, error) {
	var domains []cobaltapi.Domain
	if err := c.get(ctx, fmt.Sprintf("/api/projects/%s/domains", project), &domains); err != nil {
		return nil, err
	}
	return domains, nil
}

func (c *Client) AddDomain(ctx context.Context, project string, req cobaltapi.DomainAddRequest) (*cobaltapi.Domain, error) {
	var d cobaltapi.Domain
	if err := c.post(ctx, fmt.Sprintf("/api/projects/%s/domains", project), req, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (c *Client) RemoveDomain(ctx context.Context, project, domain string) error {
	return c.del(ctx, fmt.Sprintf("/api/projects/%s/domains/%s", project, domain))
}
