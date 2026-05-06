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
│   ├── github/     (planned)   GitHub App auth, webhook handling, repo fetch
│   ├── store/      (planned)   sqlite + embedded SQL migrations
│   ├── discofile/  (planned)   parser for the per-repo deploy config
│   ├── logs/       (planned)   log streaming
│   └── encryption/ (planned)   for env var values at rest
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
- **Errors**: `errors.Is`/`errors.As`, no panics in request paths, structured logging at the boundary.
- **Database**: SQLite. Plain SQL migrations under `migrations/`, embedded via `go:embed`, applied at daemon startup.

## Caddy reconciler

Disco's Caddy integration is purely imperative — every operation is a single REST call to Caddy's admin API over a unix socket, addressed by `@id` references in the live config. There is no desired-state reconciler; on-disk and in-memory config can drift on rollback failures (upstream issue #97).

**v1 plan:** match disco's imperative model 1:1. Same socket path, same `@id` keys (`disco-project-{name}`, `disco-project-handler-{name}`, ...), same payload shapes. Cleanest path to feature parity, and keeps the door open to adopting an existing disco-managed Caddy instance.

**Later:** layer a separate reconciler (`internal/server/caddy/reconcile.go`) that periodically diffs desired-state against the admin API + on-disk config and converges. Will fix #97 properly. The `caddy.Client` interface is shaped to support this without rewriting callers.

## License

TBD. Will be decided before any external release. Until then the repo is private.

## Reference

- `tmp/disco-daemon/` and `tmp/disco-cli/` are shallow clones of upstream, gitignored, used as **behavior reference only**. We read upstream to understand *what* it does (request shapes, state machines, edge cases), then implement from notes — not by translating files. This keeps cobalt out of derivative-work territory under upstream's GPL-3.0.
