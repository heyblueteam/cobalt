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
- [x] Architecture doc and command audit
- [x] Plan doc

## 1. Storage (`internal/server/store`)

- [ ] SQLite driver wired up (`modernc.org/sqlite` — pure Go, no CGO)
- [ ] Migrations runner (`go:embed` + plain SQL files in `migrations/`)
- [ ] Connection lifecycle, WAL mode, busy timeout
- [ ] Schema: `projects`, `deployments`, `env_vars`, `domains`, `apikeys`, `apikey_invites`, `github_apps`, `github_app_installations`, `github_app_repos`, `nodes`, `command_runs`
- [ ] CRUD methods per resource (typed, no string SQL in callers)
- [ ] Env-value encryption at rest (AES-GCM with a key stored on disk)
- [ ] Tests against a temp-file SQLite

## 2. Cobaltfile (`internal/server/cobaltfile`)

- [ ] Parser for `cobalt.json` at repo root
- [ ] Service definition: `port`, `image`, `command`, `extraSwarmParams`, `extraRunParams`
- [ ] Hook definitions: `hook:deploy:start:before`, `hook:deploy:start:after`
- [ ] Cron and CGI handler types
- [ ] Schema validation with actionable error messages
- [ ] Golden-file tests

## 3. Caddy admin client (`internal/server/caddy`)

- [ ] HTTP client over Unix socket (`net.Dialer` + `http.Transport`)
- [ ] Initial-config writer (`/initconfig/config.json`)
- [ ] Add / remove / patch project route by `@id` (`cobalt-project-{name}`)
- [ ] Update domains for project (`cobalt-project-hosts-{name}`)
- [ ] Swap upstream during deploy (`cobalt-project-handler-{name}`)
- [ ] Apex / www redirect helpers
- [ ] Static-site `file_server` handler swap
- [ ] Integration test against a real Caddy container

## 4. Docker wrapper (`internal/server/docker`)

- [ ] Build image via `docker build` shell-out (with `--no-cache` and `--secret` flags)
- [ ] Swarm service create / update / remove
- [ ] Container create / run / exec for hooks and `cobalt run`
- [ ] Volume create / inspect / export / import
- [ ] Image cleanup of orphaned tags
- [ ] Pass-through for `extraSwarmParams` and `extraRunParams`
- [ ] Tests with a mock docker CLI fixture

## 5. GitHub App (`internal/server/github`)

- [ ] App JWT generation (RS256 with private key)
- [ ] Installation access-token exchange + cache with TTL
- [ ] Webhook signature verification (HMAC-SHA256, constant-time compare)
- [ ] Push-event dispatch → enqueue deployment
- [ ] Installation / repo listing API
- [ ] Repo fetch via `git clone` with installation token
- [ ] Prune flow (sync local DB with GitHub state)
- [ ] Tests with `httptest` mocking GitHub API

## 6. Deployment flow (`internal/server/deploy`)

- [ ] State machine: `queued` → `fetching` → `building` → `swapping` → `success` / `failed`
- [ ] Run before-deploy hook in a one-shot container
- [ ] Start new service in Swarm, poll for healthy port
- [ ] Caddy upstream swap (atomic point of cutover)
- [ ] Run after-deploy hook
- [ ] Rollback on failure: revert Caddy upstream, stop new service
- [ ] Cancel running deployment
- [ ] Per-deployment log stream into store
- [ ] Tests against mock docker + caddy

## 7. HTTP API (`internal/server/api`)

- [ ] API-key auth middleware
- [ ] Structured request logging middleware (per-request `slog` fields)
- [ ] Resource handlers: deployments, projects, env, domains, scale, run, logs, volumes, github, nodes, meta, apikeys, invites
- [ ] GitHub webhook receiver
- [ ] Server-Sent Events for log streaming
- [ ] OpenAPI / handwritten reference doc

## 8. CLI subcommands (`cmd/cobalt`)

- [ ] HTTP client (`internal/client`) wrapping `pkg/cobaltapi` types
- [ ] Local config (`internal/cliconfig`) for `~/.cobalt/config.json`
- [ ] `cobalt init <user>@<host>` — bootstrap a server
- [ ] `cobalt deploy` and `deploy output|list|cancel`
- [ ] `cobalt projects list|add|remove`
- [ ] `cobalt env list|get|set|remove`
- [ ] `cobalt domains list|add|remove`
- [ ] `cobalt logs`
- [ ] `cobalt run`
- [ ] `cobalt scale get|set`
- [ ] `cobalt github apps list|add|manage|prune` and `github repos list`
- [ ] `cobalt nodes list|add|remove`
- [ ] `cobalt meta info|upgrade|host`
- [ ] `cobalt volumes list|export|import`
- [ ] `cobalt apikeys list|remove`
- [ ] `cobalt invite create|accept`
- [ ] `cobalt servers list|remove`

## 9. Distribution

- [ ] Docker image pushed to GHCR on tag (`ghcr.io/heyblueteam/cobalt`)
- [ ] Homebrew tap formula in `heyblueteam/homebrew-tap`
- [ ] `cobalt meta upgrade` pulls latest GHCR image and restarts
- [ ] Release notes generated from conventional-commit messages

## 10. Production cutover

- [ ] Side-by-side run on a low-risk service (dozzle) for 1–2 weeks
- [ ] Cutover plan for `api` and `app-next` (the high-risk services)
- [ ] Migration tool: import existing upstream sqlite state → cobalt schema
- [ ] Plan for handing over Caddy state without downtime
- [ ] Decommission upstream daemon
- [ ] Update `/Users/manny/blue/docs/disco.md` references to point at cobalt
- [ ] Decision on open-sourcing
