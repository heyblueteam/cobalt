package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/heyblueteam/cobalt/internal/client"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// Project is a created project with deterministic teardown. Get one
// from NewProject; it registers a t.Cleanup that deletes the
// project (cascading domains, deployments, etc.) unless
// COBALT_E2E_KEEP is set.
type Project struct {
	t       *testing.T
	env     Env
	client  *client.Client
	Name    string
	Branch  string
	GitHub  string
}

// NewProject creates a uniquely-named project pointing at the e2e
// fixture repo. The name embeds the test name + a unix timestamp so
// concurrent runs and abandoned cleanup leftovers don't collide.
//
// scenario should be a short slug (e.g. "primary-deploy",
// "with-www") used to label the project on the daemon for easier
// post-mortem when COBALT_E2E_KEEP=1.
func NewProject(t *testing.T, env Env, scenario string) *Project {
	t.Helper()
	name := projectName(scenario)
	cl := env.Client()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := cl.CreateProject(ctx, cobaltapi.ProjectCreateRequest{
		Name:       name,
		GithubRepo: env.FixtureRepo,
		Branch:     "main",
	})
	if err != nil {
		t.Fatalf("create project %s: %v", name, err)
	}
	p := &Project{
		t:      t,
		env:    env,
		client: cl,
		Name:   name,
		Branch: "main",
		GitHub: env.FixtureRepo,
	}
	t.Cleanup(func() {
		if env.Keep {
			t.Logf("COBALT_E2E_KEEP set — leaving project %s on %s", name, env.Host)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := cl.DeleteProject(ctx, name); err != nil {
			t.Logf("cleanup: delete project %s: %v", name, err)
		}
	})
	return p
}

// Client returns the underlying API client. Provided for tests that
// need to call methods not surfaced as helpers on Project.
func (p *Project) Client() *client.Client { return p.client }

// AddDomain attaches a primary domain to the project.
func (p *Project) AddDomain(name string) (*cobaltapi.Domain, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return p.client.AddDomain(ctx, p.Name, cobaltapi.DomainAddRequest{Name: name})
}

// AddRedirect attaches a 301 redirect domain pointing at an
// existing primary on the project.
func (p *Project) AddRedirect(name, to string) (*cobaltapi.Domain, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return p.client.AddDomain(ctx, p.Name, cobaltapi.DomainAddRequest{
		Name:       name,
		RedirectTo: to,
	})
}

// RemoveDomain removes a domain from the project. The daemon
// cascades any redirects pointing at the removed primary.
func (p *Project) RemoveDomain(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return p.client.RemoveDomain(ctx, p.Name, name)
}

// ListDomains returns the project's current domain set.
func (p *Project) ListDomains() ([]cobaltapi.Domain, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return p.client.ListDomains(ctx, p.Name)
}

func projectName(scenario string) string {
	slug := strings.ReplaceAll(strings.ToLower(scenario), "_", "-")
	slug = strings.ReplaceAll(slug, "/", "-")
	return fmt.Sprintf("e2e-%s-%d", slug, time.Now().Unix())
}
