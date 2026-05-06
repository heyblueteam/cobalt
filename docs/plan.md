# Cobalt — Implementation Plan

High-level roadmap for getting cobalt to production-ready feature parity with Blue's current deployment needs.

Each section is roughly one porting unit (a self-contained PR or two). Order is intentional: lower-risk subsystems first, deployment flow last so we have all its dependencies before wiring it up.

---

## 0. Scaffold

- [x] Single-binary cobra structure with `cobalt server` subcommand
- [x] `/healthz` endpoint, `slog` JSON logging, graceful shutdown
- [x] Dockerfile (distroless), `.dockerignore`
- [x] GitHub Actions CI: vet, build, test (race), golangci-lint
- [x] goreleaser config
- [x] Architecture doc, command audit, plan

## 1. Cross-cutting foundations

Cross-cutting conventions to lock in before we have many callers depending on them.

- [ ] CLI flag conventions: `--yes` for destructive ops, `--json` on every list command (locked in spec; enforced as commands land)
- [x] **Project context resolver** (issue cli#117 track 2): `--project` flag → `COBALT_PROJECT` env → `cobalt.json` in cwd → `currentProject` for the active server in cliconfig
- [ ] Positional subjects, scope as flag: `cobalt projects add myapp`, not `--name myapp` (convention; enforced as commands land)
- [ ] Standard error wrapping helper (`fmt.Errorf("step: %w", err)`) and a top-level `cobalt`-flavored error renderer
- [x] Daemon middleware: Bearer-token auth (`Authorization: Bearer <apiKey>`), structured request logging, panic recovery, request ID
- [x] Local CLI config (`~/.cobalt/config.json`) with multi-server support and `CurrentProject` per server

### Pre-flight verifications

- [ ] **Confirm Blue does not use upstream's CORS origins allowlist.** Search the `api` repo for any code that calls disco's `/api/cors-origins` endpoint, then check Caddy config on `server.blue.cc` for any per-project CORS routes. If found, surface the use case and decide whether to port. Default assumption: not used (CORS handled in app code).

## 2. Storage (`internal/server/store`)

- [x] SQLite driver (`modernc.org/sqlite` — pure Go, no CGO)
- [x] Migrations runner (`go:embed` plain SQL files in `internal/server/store/migrations/`)
- [x] Connection lifecycle, WAL mode, busy timeout, foreign keys ON, NORMAL sync
- [x] Schema: `projects`, `deployments`, `env_vars`, `domains`, `apikeys`, `apikey_invites`, `github_apps`, `github_app_installations`, `github_app_repos`, `pending_github_apps`, `command_runs` (apikey_usage folded into `apikeys.last_used_at`)
- [ ] CRUD methods per resource (added per-resource as endpoints land)
- [ ] AES-GCM env-value encryption at rest, key on disk in `--data-dir` (added when env endpoint lands)
- [x] Tests against a temp-file SQLite

## 3. Cobaltfile (`internal/server/cobaltfile`)

Per-repo `cobalt.json` parser.

- [x] Service definition: `type`, `image`, `port`, `command`, `build`, `publicPath`, `publishedPorts`, `volumes`, `schedule`, `exposedInternally`, `timeout`, `health`, `extraSwarmParams`, `extraRunParams`
- [x] Hook services validated: `hook:deploy:start:before`, `hook:deploy:start:after` must be type=command with non-empty command
- [x] Cron service type with per-field cron schedule validation
- [x] Static-site service type (publicPath + image-not-required logic)
- [x] Default-image auto-injection mirrors upstream rule
- [x] Schema validation with actionable error messages, strict unknown-field rejection
- [x] Comprehensive tests: defaults, Blue's actual API/app shapes, all error paths

## 4. Caddy admin client (`internal/server/caddy`)

Imperative model matching upstream behavior, with `@id` keys keyed by the project's stable `id` (not its display name) per the identity/display split — see `docs/architecture.md`.

- [ ] HTTP client over Unix socket
- [ ] Initial-config writer (`/initconfig/config.json`)
- [ ] Add / remove / patch project route by `@id` (`cobalt-project-{id}`)
- [ ] Update domains for project (`cobalt-project-hosts-{id}`)
- [ ] Swap upstream during deploy (`cobalt-project-handler-{id}`) — the deploy cutover
- [ ] Apex / www redirect helpers
- [ ] Static-site `file_server` handler swap
- [ ] Integration test against a real Caddy container

## 5. Docker wrapper (`internal/server/docker`)

Per the identity/display split, every docker service / container / network created by cobalt carries **both** labels:
- `cobalt.project.id={id}` — stable, used for every internal lookup / filter
- `cobalt.project.name={name}` — display, for humans running `docker ps --filter`

- [ ] Build image (`--no-cache`, `--secret` for env-as-build-arg)
- [ ] Swarm service create / update / remove / rolling update with both labels
- [ ] Container create / run / exec for hooks and `cobalt run` with both labels
- [ ] Lookups (`list_services_for_project`, `list_networks_for_project`, etc.) filter by `cobalt.project.id`
- [ ] Volume create / inspect / export / import
- [ ] Image cleanup of orphaned tags (background worker, id-keyed query)
- [ ] Pass-through for `extraSwarmParams` and `extraRunParams`
- [ ] Tests with a mock docker CLI fixture

## 6. GitHub App (`internal/server/github`)

- [ ] App JWT generation (RS256 with private key)
- [ ] Installation access-token exchange + cache with TTL
- [ ] Webhook signature verification (HMAC-SHA256, constant-time compare)
- [ ] Push event dispatch → enqueue deployment
- [ ] Installation / repo listing
- [ ] Repo fetch via `git clone` with installation token
- [ ] Prune flow (sync local DB with GitHub state)
- [ ] Tests with `httptest` mocking GitHub

## 7. Background workers (`internal/server/worker`)

Async jobs the daemon runs on a schedule. Cron tasks are keyed by `project_id` so renames don't disturb the scheduler.

- [ ] Worker registry with cron-style scheduling, id-keyed task table
- [ ] Image cleanup (hourly — prune unused tagged images, filter by `cobalt.project.id`)
- [ ] Project-level service crons (per-project schedules from cobalt.json)
- [ ] Tests with a mock clock

## 8. Deployment flow (`internal/server/deploy`)

- [ ] State machine: `queued` → `fetching` → `building` → `swapping` → `success` / `failed`
- [ ] Run before-deploy hook in a one-shot container
- [ ] Start new service in Swarm, poll for healthy port
- [ ] Caddy upstream swap (atomic point of cutover)
- [ ] Run after-deploy hook
- [ ] Rollback on failure: revert Caddy upstream, stop new service
- [ ] Cancel running deployment
- [ ] Per-deployment log stream into store + live SSE fan-out
- [ ] Tests against mock docker + caddy

## 9. HTTP API (`internal/server/api`)

- [ ] Resource handlers: deployments, projects, env, domains, scale, run, logs, volumes, github, meta, apikeys, invites
- [ ] GitHub webhook receiver
- [ ] Server-Sent Events for log + deploy-output streams
- [ ] WebSocket endpoint for `cobalt run` (TTY + resize)
- [ ] Handwritten API reference doc

## 10. CLI subcommands (`cmd/cobalt`)

CLI surface follows `docs/cobalt-cli-commands.md`. All commands honor the project context resolver from §1. Destructive commands require `--yes` or interactive confirmation. List commands accept `--json`.

- [ ] HTTP client (`internal/client`) wrapping `pkg/cobaltapi`
- [ ] Local config (`internal/cliconfig`) for `~/.cobalt/config.json`
- [ ] `cobalt use <project>` — set default project for current server
- [ ] `cobalt init <user>@<host>` — bootstrap a server
- [ ] `cobalt deploy [--commit] [--no-cache] [--file]`
- [ ] `cobalt deployments list|cancel|output`
- [ ] `cobalt projects list|add|remove|rename` (rename is cheap thanks to identity/display split — see architecture)
- [ ] `cobalt env list|get|set|remove`
- [ ] `cobalt domains list|add|remove`
- [ ] `cobalt logs` (SSE stream, always-follow)
- [ ] `cobalt run [--service] [--timeout]` (WebSocket-based, TTY support)
- [ ] `cobalt scale list|get|set`
- [ ] `cobalt github apps list|add|manage|prune` and `github repos list`
- [ ] `cobalt meta info|upgrade|host` (with `--image`, `--dont-pull`)
- [ ] `cobalt volumes list|export|import` (with `--output`, `--input`, `--force`)
- [ ] `cobalt apikeys list|remove`
- [ ] `cobalt invite create|accept` (with `--show-only`)
- [ ] `cobalt servers list|remove`

## 11. Distribution

- [ ] Docker image pushed to GHCR on tag (`ghcr.io/heyblueteam/cobalt`)
- [ ] Homebrew tap formula in `heyblueteam/homebrew-tap`
- [ ] `cobalt meta upgrade` pulls latest GHCR image and restarts
- [ ] Release notes generated from conventional-commit messages

## 12. Production cutover

- [ ] Side-by-side run on a low-risk service (dozzle) for 1–2 weeks
- [ ] Cutover plan for `api` and `app-next`
- [ ] Migration tool: import upstream sqlite state → cobalt schema
- [ ] Plan for handing over Caddy state without downtime
- [ ] Decommission upstream daemon
- [ ] Update `/Users/manny/blue/docs/disco.md` references to point at cobalt
- [ ] Decision on open-sourcing

---

## Resolved decisions

- **Tunnels** — dropped. Blue debugs via `cobalt run` + dozzle; no separate SSH-tunnel feature.
- **Project key-values** — dropped. Env vars cover Blue's needs.
- **CORS origins allowlist** — dropped, **with verification task in §1** to confirm against the api repo and `server.blue.cc` before cutover.
- **CGI handler service type** — dropped. Blue runs only long-lived services + crons.
- **Auth model** — Bearer tokens (`Authorization: Bearer <apiKey>`). No basic auth, no JWT.
- **`nodes` (Swarm cluster membership)** — **deferred to post-v1**, not dropped. Blue runs everything on a single host (`server.blue.cc`); multi-host swarms are unused today. Add when Blue actually splits across hosts. Estimated effort when needed: ~200 LOC (SSH + `docker swarm join` shell-out) plus a `nodes` table in store and three CLI commands.
