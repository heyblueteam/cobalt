# Cobalt CLI — Command Audit

Audit of every command in upstream `letsdiscodev/cli`, with our decision on whether v1 of cobalt's CLI ships it.

Source of truth for upstream commands: file structure under `tmp/disco-cli/src/commands/`.
Source of truth for what Blue actually uses: `/Users/manny/blue/docs/disco.md`.

## Naming convention

Cobalt uses **space-separated** subcommands, not colons. (See upstream issue [cli#117](https://github.com/letsdiscodev/cli/issues/117).)

- `disco projects:add` → `cobalt projects add`
- `disco env:set` → `cobalt env set`
- `disco github:apps:list` → `cobalt github apps list`

Cobra-native, matches `gh`/`kubectl`/`flyctl`. Colon forms are not aliased — clean break.

## Decision legend

- **YES** — ship in v1
- **NO** — Blue doesn't use it; skip
- **LATER** — useful but not blocking v1

---

## Top-level

| Command | v1 | Notes |
|---|---|---|
| `cobalt init <user>@<host>` | YES | Bootstrap a new server (install daemon via Docker, set up Caddy, generate first API key) |
| `cobalt deploy --project <p>` | YES | Trigger deploy of latest commit on tracked branch |
| `cobalt deploy --project <p> --commit <sha>` | YES | Deploy a specific commit |
| `cobalt deploy --project <p> --no-cache` | YES | Force docker build without cache (we wrote upstream PR #93 for this — bake it in from day one) |
| `cobalt logs [--project <p>] [--service <s>]` | YES | Stream daemon or service logs |
| `cobalt run --project <p> [--service <s>] [--timeout <s>] "<cmd>"` | YES | Exec a command in a project container |

## deploy subcommands

| Command | v1 | Notes |
|---|---|---|
| `cobalt deploy output --project <p>` | YES | Show output of latest deploy |
| `cobalt deploy output --project <p> --deployment <n>` | YES | Show output of a specific deploy |
| `cobalt deploy list --project <p>` | YES | List deploys |
| `cobalt deploy cancel --project <p>` | YES | Cancel a stuck deploy |

## projects

| Command | v1 | Notes |
|---|---|---|
| `cobalt projects list` | YES | |
| `cobalt projects add --name <n> --github <org/repo> --branch <b> --domain <d>` | YES | |
| `cobalt projects remove <n>` | YES | |
| `cobalt projects move --project <p> --from <s1> --to <s2>` | LATER | Cross-server migration; rare for Blue (single-server projects) |
| `cobalt projects rename <old> <new>` | LATER | Net-new feature (upstream issue #101); needs identifier/display-name split. Worth doing but not v1 |

## env

| Command | v1 | Notes |
|---|---|---|
| `cobalt env list --project <p>` | YES | |
| `cobalt env get --project <p> <KEY>` | YES | |
| `cobalt env set --project <p> KEY=val [...]` | YES | Triggers redeploy |
| `cobalt env remove --project <p> <KEY>` | YES | Triggers redeploy |

At build time each var is exposed two ways: per-key at `/run/secrets/KEY`
(Dockerfile opts in with `RUN --mount=type=secret,id=KEY ...`), and as a
single aggregate at `/run/secrets/.env` containing every var in dotenv
form (drop-in for disco-era Dockerfiles that do
`RUN --mount=type=secret,id=.env cp /run/secrets/.env .env && ...`).
Values containing newlines, quotes, or backslashes are double-quoted in
the aggregate so the file stays parseable.

## domains

| Command | v1 | Notes |
|---|---|---|
| `cobalt domains list --project <p>` | YES | |
| `cobalt domains add <domain> --project <p>` | YES | Caddy auto-provisions SSL via Let's Encrypt |
| `cobalt domains remove <domain> --project <p>` | YES | |

## scale

| Command | v1 | Notes |
|---|---|---|
| `cobalt scale get --project <p>` | YES | Show current replicas per service |
| `cobalt scale set --project <p> web=3 [worker=2 ...]` | YES | Set replica counts (current deployment only — for a persistent floor across deploys, set `minReplicas` in `cobalt.json`) |

## github

| Command | v1 | Notes |
|---|---|---|
| `cobalt github apps list` | YES | List connected GitHub Apps |
| `cobalt github apps add --organization <org>` | YES | Register a new GitHub App |
| `cobalt github apps manage <org>` | YES | Open the GitHub App settings page in a browser |
| `cobalt github apps prune` | YES | Sync local DB with GitHub (fix stale state) |
| `cobalt github repos list` | YES | List repos the connected app(s) can access |

## nodes (Swarm cluster)

| Command | v1 | Notes |
|---|---|---|
| `cobalt nodes list` | YES | |
| `cobalt nodes add <user>@<host>` | YES | Join a node to the Swarm |
| `cobalt nodes remove <node>` | YES | |

## meta

| Command | v1 | Notes |
|---|---|---|
| `cobalt meta info` | YES | Server version, hostname, etc |
| `cobalt meta upgrade` | YES | Upgrade daemon to latest image |
| `cobalt meta host <domain>` | YES | Set the daemon's own hostname for Caddy |
| `cobalt meta stats` | LATER | CPU/RAM stats — nice-to-have, not in critical flow |

## volumes

| Command | v1 | Notes |
|---|---|---|
| `cobalt volumes list --project <p>` | YES | |
| `cobalt volumes export --project <p> --volume <v>` | YES | |
| `cobalt volumes import --project <p> --volume <v>` | YES | |

## apikeys

| Command | v1 | Notes |
|---|---|---|
| `cobalt apikeys list` | YES | |
| `cobalt apikeys remove <key>` | YES | |

## invite

| Command | v1 | Notes |
|---|---|---|
| `cobalt invite create <name>` | YES | Generate an invite URL for a teammate |
| `cobalt invite accept <url>` | YES | Accept an invite (adds the disco server to local CLI config) |

## discos (local CLI config — multiple servers)

| Command | v1 | Notes |
|---|---|---|
| `cobalt discos list` | YES | Blue has multiple servers; CLI tracks them in `~/.cobalt/config.json` |
| `cobalt discos remove <name>` | YES | |

> Naming nit: should we rename `discos` → `servers` in cobalt? `cobalt servers list` reads more naturally and avoids the upstream's self-reference. **Decision: yes — rename to `servers` in v1.** No backwards compat to preserve.

## syslog

| Command | v1 | Notes |
|---|---|---|
| `cobalt syslog *` | NO | Blue uses dozzle for logs — no remote syslog forwarding |

## registries

| Command | v1 | Notes |
|---|---|---|
| `cobalt registries *` | NO | Blue doesn't use private container registries |

## postgres (disco's bundled Postgres addon)

| Command | v1 | Notes |
|---|---|---|
| `cobalt postgres *` | NO | Blue uses MySQL on dedicated DB hosts (db1/db2) via ProxySQL on each app server. No use case for disco's pg addon |

---

## Summary

- **v1 ships:** 45 subcommands across 11 top-level groups
- **Skipped entirely:** `syslog`, `registries`, `postgres` (Blue doesn't use them)
- **Deferred:** `projects move`, `projects rename`, `meta stats` (low frequency / net-new feature work)
- **Renamed:** `discos` → `servers`
- **Folded in from open upstream PRs:** `--no-cache` flag (PR #93)

The cut from upstream's command surface is roughly 30%, which matches the rough "we only need our subset" estimate.
