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

- [x] HTTP client over Unix socket (with `NewHTTPClient` for tests against `httptest.Server`)
- [x] Initial-config writer (`/initconfig/config.json`) — standalone (`:443`) and behind-tunnel (`:80`) modes
- [x] Add / remove / patch project route by `@id` (`cobalt-project-{id}`)
- [x] Update domains for project (`cobalt-project-hosts-{id}`)
- [x] Swap upstream during deploy (`cobalt-project-handler-{id}`) — the deploy cutover
- [x] Static-site `file_server` handler swap (`/cobalt/srv/{name}/deployments/{n}`)
- [x] Apex / www redirect helpers
- [x] `SetDomainsForProject` high-level reconciler (create / update / remove based on existing state)
- [x] `IsNotFound` helper for 404 distinction
- [x] Tests: 18 covering wire shapes, lifecycle, errors, init config
- [ ] Integration test against a real Caddy container (deferred to §12 cutover)

## 5. Docker wrapper (`internal/server/docker`)

Per the identity/display split, every docker service / container / network created by cobalt carries **both** labels:
- `cobalt.project.id={id}` — stable, used for every internal lookup / filter
- `cobalt.project.name={name}` — display, for humans running `docker ps --filter`

- [x] Build image (`--no-cache`, `--secret` for env-as-build-arg, deterministic argv ordering)
- [x] Swarm service create / scale / list / remove (no in-place updates — new deployment per release)
- [x] Container `Run` / `Exec` / `RemoveContainer` / `ContainerExists` / `PullImage`
- [x] Lookups (`ListServicesForProject`, `ListServicesForDeployment`, `ListVolumesForProject`) filter by `cobalt.project.id`
- [x] Volume create (idempotent) / list / export / import
- [x] Image list (parses tag → deployment number) / remove (treats missing as success)
- [x] Network create / exists / connect / disconnect (no remove — moby#37338 leaks IPs, matches upstream)
- [x] Pass-through for `extraSwarmParams` and `extraRunParams` via `SplitParams`
- [x] `WaitForServiceReady` with 3×replicas-shutdown fail-fast and overall timeout
- [x] `Runner` interface for testability + `ExecRunner` production impl
- [x] Tests with a fake runner: 30+ tests covering argv shape, label injection, idempotent missing-resource handling, deterministic env/secret ordering

## 6. GitHub App (`internal/server/github`)

- [x] App JWT generation (RS256, stdlib crypto, supports PKCS#1 + PKCS#8 PEMs, 30s expiry)
- [x] Installation access-token exchange (`MintInstallationToken`)
- [x] `InstallationToken.Valid()` honors 5-min refresh margin
- [x] Webhook signature verification (HMAC-SHA256, constant-time compare via `hmac.Equal`)
- [x] Webhook event types (push, installation, installation_repositories) with parsers
- [x] PushEvent helpers (`Branch()`, `IsBranchDelete()`)
- [x] Manifest conversion (`ConvertManifestCode`)
- [x] Manifest builder (`BuildManifest`) for App registration flow
- [x] Installation URL builder (org vs user)
- [x] Repo listing with pagination (`ListInstallationRepos`)
- [x] App-still-exists probe (`AppExists`)
- [x] Repo clone URL builder (`x-access-token` placeholder; never logged)
- [x] Migration `0002_github_app_extras.sql`: token cache columns, app html_url + name, pending app expires_at
- [x] Tests with `httptest`-backed fakeAPI: 25+ covering JWT round-trip, signature verify, parsers, pagination, error mapping
- [ ] HTTP handlers for the manifest flow (lands in §9 API)
- [ ] Webhook receiver wiring (lands in §9 + §8 deploy enqueue)
- [ ] Token cache integrated with store (`MintInstallationToken` → `github_app_installations.access_token`)
- [ ] Prune flow (lands with §10 CLI when we have the command)
- [ ] Git clone helper using `CloneURL` (lands in §8 deploy fetch step)

## 7. Background workers (`internal/server/worker`)

Async jobs the daemon runs on a schedule. Cron tasks are keyed by `project_id` so renames don't disturb the scheduler.

- [x] `Scheduler` wrapping `robfig/cron/v3` with slog logger, panic recovery, Start/Stop/Schedule/Remove
- [x] Image cleanup task (logic) — sweep all projects, drop image tags whose deployment number is no longer active, log-and-continue on per-project failure
- [x] Pending-GitHub-app cleanup task (logic) — drop rows past `expires_at` so abandoned manifest flows don't accumulate
- [x] Store CRUD for the worker's needs: `ListProjects`, `CreateProject`, `GetProjectByName`, `RenameProject`, `DeleteProject`, `ActiveDeploymentNumbers`, `CreateDeployment`, `SetDeploymentStatus`, `DeleteExpiredPendingApps`
- [x] Tests: scheduler (fires, removes, panics survived, Stop waits for in-flight, double-start no-op), image cleanup (active retained, non-active removed, per-project errors don't halt sweep, remove errors don't halt), pending-app cleanup, store CRUD against real sqlite
- [ ] Project-level service crons (registered after each successful deploy from cobaltfile services with `type: cron`) — lands with §8 deploy flow
- [ ] Wire the scheduler into `server.Run` (daemon startup) — lands with §9 API/wiring

## 8. Deployment flow (`internal/server/deploy`)

The blue line through everything. Splitting into 4 sub-PRs because §8 is too big for one. Each sub-PR is independently reviewable and ships its own tests. The plan also bakes in the upstream-improvements identified in the audit:

- **(A) Verify Caddy PATCH** — GET back the upstream value after PATCH; retry with backoff on drift; fail loudly if final read still wrong.
- **(B) Caddy convergence loop** — DB-stored desired state + periodic reconciler. Root fix for upstream issue #97.
- **(C) Docker HEALTHCHECK status** — read `Status.Health` before cutover, not just port-poll. Eliminates "port open but app crashing" 502s.
- **(D) Two-phase commit** — prepare (start + healthcheck + verify) then commit (Caddy swap + verify). Abort cleanly on commit failure.
- **(E) Build cache isolation** — per-project `--cache-from` / `--cache-to` paths. Removes the upstream LABEL workaround.
- **(F) Webhook dedup by `X-GitHub-Delivery`** — lands with §9 webhook receiver.
- **(G) Pre-deploy validation** — fail fast at API time on bad cobaltfile / unreachable commit / domain conflicts.
- **(H) Deploy log rotation to disk** — upstream stores unbounded in DB.

### 8a. State machine + queue ✓

- [x] Status set: `queued`, `fetching`, `building`, `swapping`, `success`, `failed`, `canceled`, `skipped` (migration `0003` relaxes the CHECK constraint; app code is source of truth via `pkg/cobaltapi.State`)
- [x] Per-project FIFO: dispatcher tracks `inflight map[projectID]CancelFunc`; only one deploy per project in flight
- [x] Newer queued supersedes older queued for same project (older → `skipped`)
- [x] Cancel: dispatcher cancels the runner's context; runner returns ctx.Err(); dispatcher writes `canceled` from the parent context
- [x] Daemon-restart recovery: `RecoverOnBoot` marks any active deploy as `failed`; queued deploys re-picked-up on dispatcher's first sweep
- [x] Pre-deploy validation (improvement G): project existence, cobaltfile parse if override provided. (Commit reachability deferred to 8b — needs the github clone path to be plumbed first.)
- [x] Tests: state classifications, queue Enqueue (monotonic per-project numbers, independent across projects, optional fields), Cancel for queued/active/terminal, dispatcher (success/failure/skip-older/one-in-flight-per-project/cancel/parallel-across-projects), recovery

### 8b. Build orchestration

- [ ] Repo fetch: `git clone --depth=N` with installation token URL (uses §6 `CloneURL`)
- [ ] Cobaltfile read + parse (uses §3)
- [ ] Per-service image build (uses §5 `docker.Build`); parallel where safe
- [ ] Build cache isolation (improvement E): `--cache-from`/`--cache-to` rooted at `/cobalt/data/buildkit-cache/{projectID}`
- [ ] Build output streamed to per-deployment log file
- [ ] Tests with fake docker runner + filesystem

### 8c. Cutover + rollback

- [ ] Network create (per-deployment, id-keyed labels)
- [ ] Service create per cobaltfile service (uses §5)
- [ ] Healthcheck wait (improvement C): poll `docker service inspect --format '{{.UpdateStatus.State}}'` and per-task `Status.Health`; fall back to port-poll if no health command
- [ ] Run before-deploy hook in one-shot container (with `extraRunParams` from cobaltfile per PR #92)
- [ ] Two-phase commit (improvement D):
  - Prepare: start new services, healthcheck, verify
  - Commit: Caddy upstream swap (uses §4) **with PATCH verification (improvement A)**
- [ ] Run after-deploy hook
- [ ] On any failure during prepare: stop new services + new network, mark `failed`, no Caddy touch
- [ ] On any failure during commit: revert Caddy to old upstream, stop new services, mark `failed`
- [ ] Stop old services only AFTER `success` status set
- [ ] Tests with fake docker + fake caddy

### 8d. Caddy convergence loop (improvement B)

- [ ] `caddy_desired_state` table: `{project_id, upstream_dial, domains[], handler_kind}`
- [ ] Update desired state inside the deploy commit transaction
- [ ] Reconciler scheduled every 30s by §7 worker
- [ ] Diff: GET Caddy live config, compare to desired, PATCH where divergent
- [ ] If GET fails or PATCH fails N times, log structured error so operators can alert
- [ ] (Future) Optionally also reconcile against on-disk `autosave.json` to catch the on-disk drift class
- [ ] Tests against fake caddy

### 8e. Per-deployment log rotation (improvement H)

- [ ] Logs written to `/cobalt/data/logs/deployments/{project_name}/{n}.log` (using project display name; on rename, leave old logs in place)
- [ ] Rotate to gzip when file exceeds 50MB or deployment is older than 30 days
- [ ] API serves logs by streaming the file (with offset support for SSE follow)
- [ ] Lands as a follow-up; not blocking 8a–8d

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
