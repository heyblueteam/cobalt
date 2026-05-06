# Cobalt

Deployment glue for Docker Swarm + Caddy, built for Blue.

Replaces our use of [Disco](https://letsdisco.dev) with a smaller, focused Go implementation tailored to Blue's stack.

## Status

Early days. Planning in progress.

## Components

- **`cobaltd`** — the daemon. Runs on each server, watches GitHub webhooks, builds images, orchestrates Docker Swarm rollouts, manages Caddy.
- **`cobalt`** — the CLI. Talks to `cobaltd` over HTTP.

## License

TBD.
