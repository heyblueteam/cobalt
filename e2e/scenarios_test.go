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

// TestSlowStartup: deploy a fixture branch where the container sleeps
// 30s before nginx accepts connections. Asserts that cobalt's
// readiness probe waits it out and the deploy succeeds — not that it
// returns "success" at t≈0 because Swarm reported the task running
// before the app was actually listening.
//
// Pre-readiness-probe behavior: deploy completed in ~17s with the
// fixture's 30s sleep happening *inside* a "successful" container
// that wasn't yet serving. With the probe, deploy time should be at
// least ~30s and the post-deploy HTTP probe should hit immediately.
func TestSlowStartup(t *testing.T) {
	env := RequireEnv(t)
	base := env.requireDomainBase(t)

	p := NewProjectOnBranch(t, env, "slow-startup", "slow-startup")
	domain := fmt.Sprintf("slow-%d.%s", time.Now().Unix(), base)
	if _, err := p.AddDomain(domain); err != nil {
		t.Fatalf("add domain: %v", err)
	}

	t0 := time.Now()
	p.DeployWithTimeout(t, 5*time.Minute)
	elapsed := time.Since(t0)

	// The fixture sleeps 30s. Allow some slack for build + container
	// boot, but deploys finishing in <20s prove the probe didn't run.
	if elapsed < 20*time.Second {
		t.Fatalf("deploy returned success in %s — readiness probe likely didn't wait for the app to actually listen",
			elapsed.Round(time.Second))
	}

	url := "https://" + domain + "/"
	if err := WaitForHTTP(url, 200, 60*time.Second); err != nil {
		t.Fatalf("slow-startup not serving after deploy: %v", err)
	}
	if err := AssertBodyContains(url, "slow-startup"); err != nil {
		t.Fatalf("body assertion: %v", err)
	}
}

// TestCrashLoop: deploy a fixture branch where the web container
// `exit 1`s on start. Asserts the deploy fails fast — pre-readiness-
// probe cobalt would hang in `swapping` for the full 5-minute
// HealthcheckTimeout because Swarm's task-state never settles.
//
// We assert the deploy reaches a terminal non-success state inside
// 4 minutes; HealthcheckTimeout itself is 5 min, so anything past
// that is the old hang behavior.
//
// Future extension: with a project-branch update API or commit
// override, we could also assert that a previous-good deployment
// keeps serving traffic across the failed deploy. Today we only
// validate the fail-fast property.
func TestCrashLoop(t *testing.T) {
	env := RequireEnv(t)

	p := NewProjectOnBranch(t, env, "crash-loop", "crash-loop")
	// Domain is required by the daemon for projects with a web
	// service; use a synthetic .test name since we never actually
	// expect HTTPS to come up.
	domain := fmt.Sprintf("crash-%d.example.test", time.Now().Unix())
	if _, err := p.AddDomain(domain); err != nil {
		t.Fatalf("add domain: %v", err)
	}

	t0 := time.Now()
	failed := p.DeployExpectingFailure(t, 4*time.Minute)
	elapsed := time.Since(t0)
	t.Logf("crash-loop deploy ended in %s after %s (expected non-success)",
		failed.Status, elapsed.Round(time.Second))

	// Hard upper bound: anything past 4 min means the daemon's
	// readiness/healthcheck wait isn't surfacing failure tightly
	// enough — the bug class this test is meant to guard against.
	if elapsed >= 4*time.Minute {
		t.Fatalf("crash-loop deploy took %s — expected fast failure", elapsed)
	}
}

// TestDeployHooks: deploy the fixture's `hooks` branch and prove all
// three things end-to-end:
//
//   - both hook:deploy:start:before and hook:deploy:start:after run,
//     in the right order;
//   - the per-project env var ($HOOK_MARKER) reaches the hook command;
//   - the before-hook's extraRunParams (--add-host …:host-gateway)
//     lands in the docker run argv (the hook checks /etc/hosts).
//
// Evidence comes from two independent channels: the deploy log (SSE)
// for the stdout sentinels, and the shared `sentinels` volume the
// hooks write to, served by the web container at /sentinels/*. If
// runHook ever silently drops a Volumes/EnvVars/ExtraParams field on
// RunOpts again, both channels go red.
func TestDeployHooks(t *testing.T) {
	env := RequireEnv(t)
	base := env.requireDomainBase(t)

	p := NewProjectOnBranch(t, env, "deploy-hooks", "hooks")

	marker := fmt.Sprintf("e2e-hooks-%d", time.Now().UnixNano())
	if err := p.SetEnv("HOOK_MARKER", marker); err != nil {
		t.Fatalf("set HOOK_MARKER: %v", err)
	}

	domain := fmt.Sprintf("hooks-%d.%s", time.Now().Unix(), base)
	if _, err := p.AddDomain(domain); err != nil {
		t.Fatalf("add domain: %v", err)
	}

	d := p.Deploy(t)

	// Channel 1: stdout sentinels in the deploy log. The before line
	// must precede the after line; otherwise the orchestrator wired
	// the hooks into the wrong phase.
	log := p.FetchDeployLog(t, d.ID, 30*time.Second)
	AssertLogContains(
		t, log,
		"HOOK-BEFORE-EXTRARUNPARAMS-OK",
		"HOOK-BEFORE-SENTINEL marker="+marker,
		"HOOK-AFTER-SENTINEL marker="+marker,
	)
	iBefore := strings.Index(log, "HOOK-BEFORE-SENTINEL")
	iAfter := strings.Index(log, "HOOK-AFTER-SENTINEL")
	if iAfter < iBefore {
		t.Errorf("after-hook stdout appeared before before-hook (before=%d after=%d)", iBefore, iAfter)
	}

	// Channel 2: the shared sentinels volume, served by web. Proves
	// runHook honors cobaltfile `volumes:` and the after-hook wrote
	// after the swap (otherwise web couldn't read after's file via
	// the same volume).
	url := "https://" + domain
	if err := WaitForHTTP(url+"/sentinels/before", 200, 60*time.Second); err != nil {
		t.Fatalf("sentinels not served: %v", err)
	}
	if got := MustGetBody(t, url+"/sentinels/before"); got != marker {
		t.Errorf("/sentinels/before: got %q, want %q", got, marker)
	}
	if got := MustGetBody(t, url+"/sentinels/after"); got != marker {
		t.Errorf("/sentinels/after: got %q, want %q", got, marker)
	}
	if got := MustGetBody(t, url+"/sentinels/before-extra-run-params"); got != "OK" {
		t.Errorf("/sentinels/before-extra-run-params: got %q, want %q (extraRunParams not threaded into argv)", got, "OK")
	}
}

// TestExposedInternallyAlias proves that a service declared with
// `exposedInternally: true` is reachable via the stable
// `{project}-{service}` DNS alias on cobalt-main. This is the
// load-bearing contract for cross-project env vars like api's
// `REDIS_HOST=redis-redis` after the Disco → Cobalt cutover.
//
// Fixture: branch `alias-internal` declares
//   - web   (container, port 3000)
//   - alpha (container, port 3000, exposedInternally: true)
//   - hook:deploy:start:after (command, runs on cobalt-main, probes
//     $EXPECTED_ALPHA_HOST via nc -z and wget)
//
// The hook is per-deploy ephemeral and attaches only to cobalt-main —
// the same network any cross-project caller would resolve over — so
// a passing probe is direct evidence the alias works for real
// callers, not just for the renderer.
//
// Note: after-hook failures are NON-FATAL in the orchestrator (it
// logs 🚨 but still marks the deploy successful), so this test
// asserts on log sentinels (ALIAS-TCP-OK + ALIAS-HTTP-OK) rather
// than deploy status. A broken alias surfaces as a missing OK
// sentinel; a NXDOMAIN or unreachable endpoint surfaces as the
// matching *-FAIL sentinel.
//
// Failure modes caught:
//   - Docker rejects `--network name=,alias=` syntax → service
//     create fails → deploy fails before reaching the hook (deploy
//     status not success, test fails in Deploy()).
//   - mainNetAlias regression → alias not registered → nc -z misses
//     → ALIAS-TCP-FAIL in log.
//   - Alias registered but pointing at a dead endpoint → nc may
//     succeed but wget fails → ALIAS-HTTP-FAIL in log.
//
// What this does NOT cover: cross-project resolution explicitly.
// Both services live in one project for fixture-setup simplicity —
// the alias-resolution code path on cobalt-main is identical
// regardless of which project the caller belongs to (swarm's
// embedded DNS doesn't have a project concept). If we ever add
// project-scoped DNS, this test will need a second project.
func TestExposedInternallyAlias(t *testing.T) {
	env := RequireEnv(t)

	p := NewProjectOnBranch(t, env, "exposed-alias", "alias-internal")

	// Project name is per-run (e.g. e2e-exposed-alias-1716480000),
	// so the hook can't hardcode the alias hostname. Thread it
	// through as an env var the after-hook references.
	expected := p.Name + "-alpha"
	if err := p.SetEnv("EXPECTED_ALPHA_HOST", expected); err != nil {
		t.Fatalf("set EXPECTED_ALPHA_HOST: %v", err)
	}

	d := p.Deploy(t)

	log := p.FetchDeployLog(t, d.ID, 30*time.Second)
	AssertLogContains(
		t, log,
		"ALIAS-PROBE host="+expected,
		"ALIAS-TCP-OK",
		"ALIAS-HTTP-OK",
	)
	// Belt-and-suspenders: explicit error if either *-FAIL sentinel
	// shows up. AssertLogContains would already fail above on the
	// missing OK, but naming the failure mode helps triage.
	if strings.Contains(log, "ALIAS-TCP-FAIL") {
		t.Errorf("after-hook reported TCP failure for alias %q — alias not registered on cobalt-main, or alpha not listening", expected)
	}
	if strings.Contains(log, "ALIAS-HTTP-FAIL") {
		t.Errorf("after-hook reached the alias %q on TCP but HTTP probe failed — alias may resolve to wrong endpoint", expected)
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
