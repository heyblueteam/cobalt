package e2e

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/heyblueteam/cobalt/internal/client"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// TestPrimaryDeploy: create a project, deploy it, assert HTTP 200
// + expected body content on the project's primary domain.
//
// Skips if COBALT_E2E_DOMAIN_BASE is unset — without DNS pointing
// at the daemon there's no way to actually reach the deploy.
func TestPrimaryDeploy(t *testing.T) {
	env := RequireEnv(t)
	base := env.requireDomainBase(t)

	p := NewProject(t, env, "primary-deploy")
	domain := fmt.Sprintf("primary-%d.%s", time.Now().Unix(), base)
	if _, err := p.AddDomain(domain); err != nil {
		t.Fatalf("add domain: %v", err)
	}

	p.Deploy(t)

	// Cert provisioning + first-request warm-up can take a beat
	// after deploy returns success. Poll briefly before asserting.
	url := "https://" + domain + "/"
	if err := WaitForHTTP(url, 200, 90*time.Second); err != nil {
		t.Fatalf("primary domain not serving: %v", err)
	}
	if err := AssertBodyContains(url, "cobalt-fixture-app"); err != nil {
		t.Fatalf("body assertion: %v", err)
	}
}

// TestApexWithWWW: add an apex domain, then add `www.<apex>` as a
// redirect to it. Assert that hitting www returns 301 → apex and
// apex serves the fixture body.
func TestApexWithWWW(t *testing.T) {
	env := RequireEnv(t)
	base := env.requireDomainBase(t)

	p := NewProject(t, env, "apex-with-www")
	// Use a 2-label domain so apexWWWPair logic treats it as apex.
	// We get this by using the immediate base as the apex itself,
	// which means COBALT_E2E_DOMAIN_BASE must itself be a real
	// 2-label apex (e.g. example.com), with wildcard DNS so
	// arbitrary subdomains resolve. Build a unique 2-label apex
	// under the test's control: <id>.<base> would be 3 labels,
	// which the daemon's apex-www logic treats as a www-of-apex
	// or subdomain. So instead, use a stable apex pattern that's
	// 2 labels: this is the operator's responsibility to set up.
	// For e2e against a wildcard like *.example.com we accept that
	// only true apex+www pairs (e.g. example.com+www.example.com)
	// exercise the apex/www pairing — subdomain pairs still work
	// at the redirect level (--redirect-to), just not the
	// helper-flag level.
	apex := fmt.Sprintf("apex-%d.%s", time.Now().Unix(), base)
	www := "www." + apex
	if _, err := p.AddDomain(apex); err != nil {
		t.Fatalf("add apex: %v", err)
	}
	if _, err := p.AddRedirect(www, apex); err != nil {
		t.Fatalf("add www redirect: %v", err)
	}

	p.Deploy(t)

	apexURL := "https://" + apex + "/"
	wwwURL := "https://" + www + "/"
	if err := WaitForHTTP(apexURL, 200, 90*time.Second); err != nil {
		t.Fatalf("apex not serving: %v", err)
	}
	// www→apex 301: Caddy emits the redirect target with the
	// original scheme and host, no path normalization.
	if err := AssertRedirect(wwwURL, apexURL); err != nil {
		t.Fatalf("www redirect: %v", err)
	}
}

// TestRedirectTo: arbitrary --redirect-to between two domains the
// project owns. The CLI flag is tested via cmd/cobalt unit tests;
// here we verify the API surface end-to-end + that a redirect
// pointing at a non-primary is rejected by the daemon.
func TestRedirectTo(t *testing.T) {
	env := RequireEnv(t)
	base := env.requireDomainBase(t)

	p := NewProject(t, env, "redirect-to")
	primary := fmt.Sprintf("primary-%d.%s", time.Now().Unix(), base)
	other := fmt.Sprintf("old-%d.%s", time.Now().Unix(), base)
	if _, err := p.AddDomain(primary); err != nil {
		t.Fatalf("add primary: %v", err)
	}

	// Pointing at a non-primary must fail.
	bogus := "does-not-exist." + base
	if _, err := p.AddRedirect(other, bogus); err == nil {
		t.Fatal("expected error redirecting to non-primary, got nil")
	} else {
		var apiErr *client.APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 {
			t.Fatalf("expected 400 APIError, got %T: %v", err, err)
		}
		if !strings.Contains(apiErr.Message, "must already exist as a primary") {
			t.Fatalf("error message missing guidance: %q", apiErr.Message)
		}
	}

	// Valid redirect installs.
	if _, err := p.AddRedirect(other, primary); err != nil {
		t.Fatalf("add redirect: %v", err)
	}

	doms, err := p.ListDomains()
	if err != nil {
		t.Fatalf("list domains: %v", err)
	}
	if !hasRedirect(doms, other, primary) {
		t.Fatalf("redirect not present in list: %+v", doms)
	}

	p.Deploy(t)

	primaryURL := "https://" + primary + "/"
	otherURL := "https://" + other + "/"
	if err := WaitForHTTP(primaryURL, 200, 90*time.Second); err != nil {
		t.Fatalf("primary not serving: %v", err)
	}
	if err := AssertRedirect(otherURL, primaryURL); err != nil {
		t.Fatalf("redirect: %v", err)
	}
}

// TestRemoveCascadesRedirects: removing a primary that has
// redirects pointing at it must drop the redirect rows in the same
// step, with no orphaned Caddy routes. We can't peek Caddy's state
// from here (would need SSH); we assert the API view, which is the
// reconciler's source of truth.
func TestRemoveCascadesRedirects(t *testing.T) {
	env := RequireEnv(t)
	// No DNS needed — pure API behavior test.

	p := NewProject(t, env, "cascade-remove")

	primary := fmt.Sprintf("primary-%d.example.test", time.Now().Unix())
	other := fmt.Sprintf("other-%d.example.test", time.Now().Unix())
	if _, err := p.AddDomain(primary); err != nil {
		t.Fatalf("add primary: %v", err)
	}
	if _, err := p.AddRedirect(other, primary); err != nil {
		t.Fatalf("add redirect: %v", err)
	}
	doms, err := p.ListDomains()
	if err != nil {
		t.Fatalf("list before remove: %v", err)
	}
	if len(doms) != 2 {
		t.Fatalf("expected 2 domains pre-remove, got %d: %+v", len(doms), doms)
	}

	if err := p.RemoveDomain(primary); err != nil {
		t.Fatalf("remove primary: %v", err)
	}

	doms, err = p.ListDomains()
	if err != nil {
		t.Fatalf("list after remove: %v", err)
	}
	if len(doms) != 0 {
		t.Fatalf("expected cascade to remove redirect too, got %d: %+v", len(doms), doms)
	}
}

func hasRedirect(domains []cobaltapi.Domain, from, to string) bool {
	for _, d := range domains {
		if d.Name == from && d.Type == cobaltapi.DomainTypeRedirect && d.RedirectTo == to {
			return true
		}
	}
	return false
}
