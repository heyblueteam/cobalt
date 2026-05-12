# cobalt end-to-end tests

This directory drives a real cobalt daemon over its HTTP API and
asserts outcomes against real deployments. It is the regression
suite for behavior that unit tests can't reach — Caddy admin
quirks, cascading domain operations, deploy state machines under
load, and so on.

## Running

You'll need:

  1. A running cobalt daemon you control. Any Linux box with
     cobalt installed works — your laptop, a $4 droplet, the
     production-shaped box you keep around for verification.
     The README at the repo root has the install one-liner.
  2. An API key for that daemon (`cobalt apikeys add` on the box).
  3. *(Optional, for the scenarios that exercise public HTTPS)* A
     wildcard DNS record pointing at the daemon, e.g.
     `*.e2e.example.com → <daemon IP>`.

Set the env vars and run:

```bash
export COBALT_E2E_HOST=cobalt.example.com
export COBALT_E2E_API_KEY=...
export COBALT_E2E_DOMAIN_BASE=e2e.example.com   # optional, see below
make e2e
```

## Environment variables

| Var                         | Required | Default                       | Purpose                                                 |
|-----------------------------|----------|-------------------------------|---------------------------------------------------------|
| `COBALT_E2E_HOST`           | yes      | —                             | Daemon hostname/IP                                      |
| `COBALT_E2E_API_KEY`        | yes      | —                             | Daemon API key                                          |
| `COBALT_E2E_DOMAIN_BASE`    | no       | —                             | Wildcard apex tests can register subdomains under       |
| `COBALT_E2E_FIXTURE_REPO`   | no       | `heyblueteam/cobalt-fixture-app` | GitHub `owner/repo` of the fixture app to deploy   |
| `COBALT_E2E_KEEP`           | no       | unset                         | If set, leave projects on the daemon after the run      |
| `COBALT_E2E_INSECURE_TLS`   | no       | unset                         | Accept any server cert — for `cobalt init --insecure-tls` daemons |

When `COBALT_E2E_HOST` is unset, every test in this package calls
`t.Skip` — `go test ./...` stays green for contributors who haven't
configured a target. Same gating idiom Terraform uses for its
acceptance tests (`TF_ACC=1`).

When `COBALT_E2E_DOMAIN_BASE` is unset, tests that require public
HTTPS skip; pure-API tests (cascade remove, etc.) still run.

`COBALT_E2E_KEEP` is the post-mortem switch: when something fails,
re-run with `COBALT_E2E_KEEP=1` and the project survives so you can
inspect Caddy state, container logs, etc.

## What's covered (Phase 1)

| Test                        | Needs DNS | Asserts                                                      |
|-----------------------------|-----------|--------------------------------------------------------------|
| `TestPrimaryDeploy`         | yes       | Project + deploy + HTTPS GET returns expected body           |
| `TestApexWithWWW`           | yes       | apex serves; `www.<apex>` 301s to apex; both certs issued    |
| `TestRedirectTo`            | yes       | arbitrary 301; daemon rejects redirects to non-primaries     |
| `TestRemoveCascadesRedirects` | no      | removing a primary cascades its redirects in the same step   |
| `TestDeployHooks`           | yes       | before+after hooks run, env+volumes+extraRunParams threaded  |

## Bringing your own host

The test suite has zero hardcoded references to Blue infra. You
can run it against:

  - your laptop (`brew install cobalt && cobalt daemon` or
    however the install path looks on your platform)
  - a throwaway VM you spin up by hand
  - a CI-managed ephemeral box

If you need help getting a daemon up, see the install
instructions in the [main README](../README.md).

## Adding scenarios

Each scenario lives in `scenarios_test.go` as a top-level `TestX`
function:

  1. Call `RequireEnv(t)` first — it gates on `COBALT_E2E_HOST`.
  2. Call `NewProject(t, env, "<short-slug>")` — registers cleanup.
  3. Drive the API via the helpers on `*Project`.
  4. Assert via `WaitForHTTP`, `AssertRedirect`, `AssertBodyContains`,
     or by inspecting return values.

Keep scenarios independent — no shared state between tests, no
ordering assumptions. Each test creates its own project; each
project is uniquely-named and torn down on completion.
