# Cobalt CLI — Command Reference

Authoritative spec for the cobalt CLI. This is the design target — what we're building, not a comparison to upstream. For the upstream-comparison audit see [`commands.md`](commands.md).

Design principles drawn from upstream issue [cli#117](https://github.com/letsdiscodev/cli/issues/117) (the user's own pitch). Cobalt has no backwards compat to preserve, so we adopt all three tracks from day one.

## Conventions

### Command structure

- **Space-separated subcommands.** `cobalt projects add`, never `cobalt projects:add`.
- **Positional subjects, scope as flags.** `cobalt projects add myapp` and `cobalt env set KEY=val` (the project is inferred). Flags are reserved for *scope* (`--project`, `--server`) or *modifiers* (`--commit`, `--no-cache`).
- **Verb consistency per topic.** Pick one vocabulary and stick to it:
  - CRUD-ish: `list / get / set / remove`
  - add/remove: `list / add / remove`
  - One topic per choice — no mixing.

### Project context

Every project-scoped command resolves the active project in this order:

1. `--project <name>` flag (explicit override)
2. `COBALT_PROJECT` environment variable
3. `cobalt.json` in the current directory or any parent (the file already exists for deployable repos)
4. `cobalt use <name>` — sets a default for the current server in `~/.cobalt/config.json`

If none resolve and the command requires a project, exit with a clear error pointing at `cobalt use`.

### Global flags

- `--server <name>` — pick which cobalt server to talk to (default from `~/.cobalt/config.json`)
- `--json` — machine-readable output for list/get commands
- `--yes` — skip confirmation prompts on destructive commands (matches `gh`, `apt`, `helm`)

### Authentication

The CLI sends `Authorization: Bearer <apiKey>` on every request. API keys are stored per-server in `~/.cobalt/config.json`.

---

## Top-level

### `cobalt deploy`

Trigger a deployment of the current project.

```
cobalt deploy [--project <p>] [--commit <sha>] [--no-cache] [--file <path>]
```

- `--commit <sha>` — deploy a specific commit instead of the latest on the tracked branch
- `--no-cache` — pass `--no-cache` to `docker build`
- `--file <path>` — upload a custom `cobalt.json` for this deploy only

Streams build + swap output to stdout. Exits 0 on success, non-zero on failure or cancellation.

### `cobalt logs`

Stream daemon or service logs. Always follows.

```
cobalt logs [--project <p>] [--service <s>]
```

- No `--project` → daemon-level logs
- With `--project` → all services in the project
- With `--service` → just that service

Press Ctrl+C to stop.

### `cobalt run`

Run a one-off command inside a project's container. Interactive (allocates a TTY, supports resize).

```
cobalt run [--project <p>] [--service <s>] [--timeout <secs>] "<command>"
```

- `--service` defaults to the project's primary `web` service
- `--timeout` defaults to 600 seconds

Implementation: WebSocket to the daemon, raw-mode TTY, exit code from the container is the CLI's exit code.

### `cobalt init`

Bootstrap a fresh server: install daemon via Docker, set up Caddy, register the first API key in local CLI config.

```
cobalt init <user>@<host> [--advertise-addr <ip>] [--identity-file <path>] [--image <ref>] [--local-image]
```

- `--advertise-addr` — fixed IP for Swarm node discovery (use on multi-NIC hosts)
- `--identity-file` — SSH private key for connecting
- `--image` — pin to a specific cobalt daemon image
- `--local-image` — use a local docker image (dev only — avoids the registry pull)

Interactive: prompts for SSH password / passphrase if needed.

### `cobalt use`

Set the default project for the current server.

```
cobalt use <project>
```

Writes `current_project` into `~/.cobalt/config.json` for the active server entry.

---

## Projects

```
cobalt projects list [--json]
cobalt projects add <name> --github <org/repo> --branch <branch> [--domain <d>]
cobalt projects remove <name> [--yes]
cobalt projects transfer <name> --from <server> --to <server>
```

Notes:
- `add` takes `<name>` as positional. `--github` and `--branch` are required.
- `remove` prompts for confirmation unless `--yes`.
- `transfer` (was `move` upstream) avoids collision with a future `rename` command.

## Deployments

Past deploys for a project. (Bare `cobalt deploy` is the action; `deployments` is the noun-topic.)

```
cobalt deployments list [--project <p>] [--json]
cobalt deployments output [--project <p>] [--deployment <n>]
cobalt deployments cancel [--project <p>]
```

- `output` with no `--deployment` shows the latest.
- `cancel` cancels the in-flight deployment, if any.

## Env

```
cobalt env list [--project <p>] [--json]
cobalt env get <KEY> [--project <p>]
cobalt env set KEY=val [KEY2=val2 ...] [--project <p>]
cobalt env remove <KEY> [--project <p>] [--yes]
```

`set` and `remove` may trigger an automatic redeploy. Output indicates whether one was kicked off.

## Domains

```
cobalt domains list [--project <p>] [--json]
cobalt domains add <domain> [--project <p>]
cobalt domains remove <domain> [--project <p>] [--yes]
```

`add` provisions a Let's Encrypt cert via Caddy automatically.

## Scale

```
cobalt scale list [--project <p>] [--json]
cobalt scale get [--project <p>] [--json]
cobalt scale set web=3 [worker=2 ...] [--project <p>]
```

`scale list` added for verb consistency (CRUD-style).

## GitHub

### `cobalt github apps`

```
cobalt github apps list [--json]
cobalt github apps add --organization <org> [--non-interactive]
cobalt github apps manage <org>
cobalt github apps prune
```

- `add` opens a browser to register a GitHub App. `--non-interactive` prints the URL and exits.
- `manage` opens the GitHub App settings page in a browser.
- `prune` syncs local DB with GitHub state (fixes "stale app" issues).

### `cobalt github repos`

```
cobalt github repos list [--json]
```

Lists every repo accessible to any connected GitHub App.

## Meta

```
cobalt meta info [--json]
cobalt meta host <domain>
cobalt meta upgrade [--image <ref>] [--dont-pull]
```

- `host` sets the daemon's own hostname for Caddy.
- `upgrade --image` pins a specific image.
- `upgrade --dont-pull` skips the docker pull (use with `--local-image` from `init`).

## Volumes

```
cobalt volumes list [--project <p>] [--json]
cobalt volumes export [--project <p>] --volume <v> [--output <path>] [--force]
cobalt volumes import [--project <p>] --volume <v> [--input <path>]
```

- `export` writes binary tar to stdout by default. `--force` skips the TTY confirmation. `--output` writes to a file.
- `import` reads from stdin by default. `--input` reads from a file. `--input` is required if stdin is a TTY.

## API keys

```
cobalt apikeys list [--json]
cobalt apikeys remove <key> [--yes]
```

## Invites

```
cobalt invite create <name> [--json]
cobalt invite accept <url> [--show-only]
```

- `create` returns a one-time URL the recipient runs `cobalt invite accept` against.
- `accept` adds the server to local CLI config. `--show-only` prints the API key without writing config.

## Servers (local CLI config)

```
cobalt servers list [--json]
cobalt servers remove <name> [--yes]
```

Manages the entries in `~/.cobalt/config.json`. (Was `discos` upstream.)

---

## Removed from upstream

These will not exist in cobalt v1:

- `postgres *` — Blue uses MySQL on dedicated DB hosts (db1/db2) via ProxySQL on each app server
- `registries *` — Blue doesn't use private container registries
- `syslog *` — Blue uses dozzle for logs
- `tunnel *` — Blue debugs via `cobalt run` + dozzle, no SSH-into-container flow needed
- `corsorigins *` — CORS handled in app code (verification pending — see plan §1)
- `cgi` service type — Blue only runs long-lived services + crons
- `keyvalues *` — env vars cover Blue's per-project storage needs

## Deferred to post-v1

- `nodes *` — Docker Swarm cluster-membership commands. Blue runs everything on a single host (`server.blue.cc`); multi-host swarms aren't used today. Will revisit if/when Blue splits services across hosts.

## Renamed from upstream

| Upstream | Cobalt | Reason |
|---|---|---|
| `disco projects:move` | `cobalt projects transfer` | avoids future `projects rename` collision |
| `disco deploy:list / deploy:cancel / deploy:output` | `cobalt deployments list / cancel / output` | `deploy` stays the action verb; `deployments` is the noun-topic |
| `disco discos list / remove` | `cobalt servers list / remove` | "discos" was self-referential; "servers" reads naturally |
| `disco scale` (no list) | `cobalt scale list / get / set` | rounds out CRUD verbs for consistency |
| `--no-input` (sometimes) | `--yes` (everywhere destructive) | matches gh / apt / helm |
