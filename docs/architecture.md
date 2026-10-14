# Architecture

## Single binary

`cobalt` is one Go binary with two modes:

- **CLI mode** (default): `cobalt deploy`, `cobalt projects list`, etc. Talks to a remote daemon over HTTP using credentials in `~/.cobalt/config.json`.
- **Daemon mode**: `cobalt server`. Long-running HTTP server on a host that owns Docker Swarm + Caddy. Started by Docker (or systemd), never by humans.

Both modes are compiled from the same source tree. They share types via `pkg/cobaltapi`. There is no separate daemon binary.

## Planned package layout

Only directories that contain real code today are listed here without `(planned)`. The rest are placeholders for the porting work.

```
cmd/
└── cobalt/              # cobra root + every subcommand
    ├── main.go
    ├── root.go
    └── server.go        # 'cobalt server' — wires Config and calls server.Run

internal/
├── server/              # the daemon
│   ├── server.go        # Config + Run(ctx, cfg)
│   ├── api/        (planned)   HTTP handlers — one file per resource
│   ├── deploy/     (planned)   deployment state machine, build, hooks, rollback
│   ├── docker/     (planned)   wrapper around `docker` / Docker API
│   ├── caddy/      (planned)   admin API client over unix socket
│   ├── github/      (planned)   GitHub App auth, webhook handling, repo fetch
│   ├── store/       (planned)   sqlite + embedded SQL migrations
│   ├── cobaltfile/  (planned)   parser for the per-repo cobalt.json deploy config
│   ├── logs/        (planned)   log streaming
│   └── encryption/  (planned)   for env var values at rest
├── client/         (planned)   HTTP client used by CLI subcommands
└── cliconfig/      (planned)   ~/.cobalt/config.json read/write

pkg/
└── cobaltapi/           # public — only package importable from outside
    └── types.go

migrations/         (planned)   plain .sql files, embedded via go:embed
test/               (planned)   integration + e2e tests against real Docker/Caddy
```

## Conventions

- **Logging**: `log/slog` with the JSON handler in production. Per-request fields (`project`, `deployment_id`, etc.) carried via context, not globals.
- **Config**: stdlib `encoding/json` for `~/.cobalt/config.json`. Daemon config is CLI flags + environment variables only — no config file.
- **HTTP**: stdlib `net/http` with Go 1.22+ pattern routing (`mux.HandleFunc("GET /path", ...)`). No third-party router.
- **Go version**: 1.25 (required by `modernc.org/sqlite`).
- **Errors**: `errors.Is`/`errors.As`, no panics in request paths, structured logging at the boundary.
- **Database**: SQLite. Plain SQL migrations under `migrations/`, embedded via `go:embed`, applied at daemon startup.

## Identity vs display

A project has two names: a **stable identifier** (its `id`) and a **display name** (its `name`). The identifier never changes; the display name is mutable via `cobalt projects rename`.

The split exists so renaming a project is cheap. Upstream conflates the two — the Caddy routes, docker labels used for lookup, and worker keys are all keyed by the project's display name, so renaming would require rewriting all of them. We avoid that by keying internal plumbing on `id` from day one.

Where each form is used:

- **Identity (`project.id`)** — DB foreign keys, Caddy `@id` route keys (`cobalt-project-{id}`, `cobalt-project-handler-{id}`, `cobalt-project-hosts-{id}`), the docker label `cobalt.project.id` used to look up services / networks / containers, worker task keys, image-cleanup filters.
- **Display (`project.name`)** — API URL paths (`/api/projects/{name}`), CLI output, docker service names (`{name}-{n}-{svc}`), docker network names (`cobalt-project-{name}-{n}`), image tags (`cobalt/project-{name}-{img}:{n}`), filesystem (`/cobalt/data/projects/{name}/`), the docker label `cobalt.project.name` used for human-facing `docker ps --filter`.

A rename then collapses to: a UNIQUE check, an `UPDATE projects SET name = ?`, `os.Rename` on the project directory, and an event. Live services keep their old display name in their `cobalt.project.name` label and on disk until next deploy recreates them — same staleness model as a not-yet-rebuilt image tag, and acceptable.

Reference: upstream design discussion in [disco-daemon issue #101](https://github.com/letsdiscodev/disco-daemon/issues/101).

## Caddy reconciler

The upstream tool's Caddy integration is purely imperative — every operation is a single REST call to Caddy's admin API over a unix socket, addressed by `@id` references in the live config. There is no desired-state reconciler; on-disk and in-memory config can drift on rollback failures.

**v1 plan:** match the upstream imperative model 1:1 (single REST calls, same payload shapes), but key `@id`s by the project's stable id (`cobalt-project-{id}`, `cobalt-project-handler-{id}`, `cobalt-project-hosts-{id}`). Caddy treats `@id` as an opaque address, so this is invisible to Caddy — it just means renames don't have to rewrite Caddy state.

**Later:** layer a separate reconciler (`internal/server/caddy/reconcile.go`) that periodically diffs desired-state against the admin API + on-disk config and converges. Will fix the rollback-drift class of bugs properly. The `caddy.Client` interface is shaped to support this without rewriting callers.

## License

TBD. Will be decided before any external release. Until then the repo is private.

## Reference

- `tmp/` holds shallow clones of the upstream tool we're replacing. Gitignored, used as **behavior reference only**. We read upstream to understand *what* it does (request shapes, state machines, edge cases), then implement from notes — not by translating files. This keeps cobalt out of derivative-work territory under upstream's GPL-3.0.

## Stable web routing with shared volumes

Set `stablePublicWeb: true` to keep the public web service name and VIP across
start-first updates. Old and new tasks overlap. A web volume is eligible only
when it explicitly declares that the application supports concurrent access:

```json
{
  "version": "1.0",
  "stablePublicWeb": true,
  "services": {
    "web": {
      "port": 3000,
      "volumes": [{
        "name": "assets",
        "destinationPath": "/var/cache/app",
        "sharedDuringUpdate": true
      }]
    }
  }
}
```

Use this for storage designed for concurrent use, such as an immutable asset
archive. Do not enable it for an embedded database or single-writer files.
Volume names stay project-scoped. Updates preserve unchanged mounts, reconcile
added/changed/removed mounts, and never delete the volume data. The normal Swarm
node-local volume constraints still apply; this does not replicate files across
nodes. Applications own file retention and concurrent-write safety.

A requested stable configuration that is ineligible still uses numbered routing
for compatibility, but deployment output and daemon logs now explain why.

Numbered web endpoints, and a stable endpoint being replaced by numbered
routing, are retired only by the reconciler after it confirms the new route and
waits its five-minute grace floor. A subsequent deploy cannot bypass that gate.
Cutover (including rollback) and reconciliation use the same per-project lock;
a tick skips busy projects instead of blocking the fleet or deleting a stable
endpoint activated by a concurrent cutover. Internal-only and static destinations
keep their existing cleanup policy because they have no public container-route reaper.
With continuous deployments, old endpoints remain until there is a five-minute
quiet window; plan capacity for that overlap. The Caddy reload grace must remain
shorter than the retirement window (the default is 30 seconds).

To run the pooled-HTTP and shared-asset regression, point `DOCKER_HOST` at a
**disposable** Docker daemon and run:

```sh
COBALT_DISPOSABLE_SWARM=1 go test -tags integration \
  ./internal/server/docker -run '^TestReconcileStableService_RealSwarm$' -v
```

The test initializes and leaves a Swarm on that daemon. It does not use a
production server. It keeps an HTTP/1.1 client connection through Caddy open
across an image update and checks that the new build and old assets remain
available without failed requests.
