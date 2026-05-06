# Cobalt

Deployment glue for Docker Swarm + Caddy, built for Blue.

A focused Go reimplementation of [Disco](https://letsdisco.dev) tailored to Blue's stack.

## Status

Pre-alpha. Scaffold only. See [`commands.md`](commands.md) for the v1 CLI surface and [`docs/architecture.md`](docs/architecture.md) for the layout.

## Build

```bash
go build -o cobalt ./cmd/cobalt
./cobalt --help
```

## Run the daemon locally

```bash
./cobalt server --addr 127.0.0.1:8080 --data-dir /tmp/cobalt-data
curl http://127.0.0.1:8080/healthz
```

## Test

```bash
go test ./...
```

## Layout

Single binary. CLI and daemon ship together — `cobalt` is the CLI, `cobalt server` runs the daemon.

```
cmd/cobalt/         # cobra CLI — all subcommands including 'server'
internal/server/    # daemon — HTTP API, deploy flow, Caddy + Docker
pkg/cobaltapi/      # request/response types shared CLI ↔ daemon
tmp/                # gitignored — read-only checkouts of upstream disco
```

See [`docs/architecture.md`](docs/architecture.md) for the planned package layout.

## License

TBD.
