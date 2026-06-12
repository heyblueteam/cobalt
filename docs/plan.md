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

- [x] Service definition: `type`, `image`, `port`, `command`, `build`, `publicPath`, `publishedPorts`, `volumes`, `schedule`, `exposedInternally`, `timeout`, `health`, `extraSwarmParams`, `extraRunParams`, `stopFirst`
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
- [x] Network cleanup task — sweep overlay networks, drop ones whose deployment number is no longer active. Filtering by `cobalt.project.id` label avoids touching unlabeled networks; deployment-numbered names sidestep moby/moby#37338 (we never re-create with the same name); docker's "active endpoints" error is the race-protection if a deploy is mid-flight.
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

### 8b. Build orchestration ✓

- [x] Migration `0004_deployment_cobaltfile_override.sql` — persists `--file` overrides so daemon restart between enqueue and pickup doesn't drop them
- [x] `TokenProvider` (cache-or-mint via DB token columns); 5-min refresh margin from `InstallationToken.Valid`
- [x] `Preparer` with `GitRunner` interface (production: `ExecGit` shells to `git`); fresh clone or fetch+set-url+checkout for existing workspaces; per-deploy commit override; per-deploy cobaltfile override
- [x] `Builder` builds each unique image once (multiple services sharing an image cause one build); skips static-only services and `build:`-overriding services; propagates env vars as `--secret`
- [x] Build cache isolation (improvement E): `BuildOpts.CacheDir` adds `--cache-from`/`--cache-to type=local` rooted at `{dataDir}/buildkit-cache/{projectID}`
- [x] Store: `GetGithubApp`, `GetGithubAppInstallation`, `SetInstallationToken`, `CreateGithubApp`, `CreateGithubAppInstallation`, `ListEnvVars`, `EnvVarMap`
- [x] Dockerfile: switched runtime from distroless → debian-slim with `git` and `docker-cli`
- [x] Tests: 19 covering cache hit/miss, mint failure, project without installation, fresh clone, fetch path, commit override, cobaltfile override, token error propagation, image dedup, cache dir wiring, static-only skip, env-secret/no-cache propagation, unknown image, docker error
- [ ] Build output streamed to per-deployment log file (lands in 8e)
- [ ] Compose Preparer + Builder into a Runner the dispatcher uses (lands in 8c with the swap)

### 8c. Cutover + rollback ✓

- [x] Per-deployment network create (id-keyed labels via §5 helpers)
- [x] `cobalt-main` shared overlay network for hooks (idempotent ensure)
- [x] Service create per cobaltfile container service (uses §5 `CreateService`)
- [x] Healthcheck wait (improvement C): `docker.WaitForServiceHealthy` reads per-task `.Status.Health == "healthy"`; falls back to task-state running for services without a healthcheck declared
- [x] Hook runner: before/after one-shot containers on `cobalt-main` with `extraRunParams` (PR #92 baked in)
- [x] Two-phase commit (improvement D): Phase 1 (prepare/build/start/healthcheck) leaves Caddy untouched; Phase 2 (commit) does the Caddy swap with PATCH verify (improvement A); on Phase 2 failure, revert + stop new services
- [x] Caddy PATCH verification (improvement A): `caddy.VerifyServeService` does PATCH then GET-with-backoff (5 attempts, ~1.5s total); returns `*PatchVerifyError` on persistent drift
- [x] Static-site / generator support: bind-mount-output approach to `c.StaticSiteDeploymentPath(...)`, `caddy.ServeStaticSite` swaps file_server handler
- [x] After-hook runs after Caddy commit; failure logs warning but does NOT fail the deploy (matches upstream)
- [x] Best-effort cleanup of old services after success (filter by deployment number prefix)
- [x] Phase 1 failure path: stop new services we did start, no Caddy touch
- [x] Phase 2 failure path: revert Caddy upstream to last successful deployment, stop new services
- [x] First-deploy revert is a no-op (no `GetLastSuccessfulDeployment` target → log only)
- [x] Tests: 9 covering happy path, prepare error, build error stops no services, healthcheck failure stops services + leaves Caddy untouched, Caddy verify failure reverts + cleans up, first-deploy-no-prior-success, after-hook failure does NOT roll back, no-web-service skips Caddy entirely, generator path with file_server swap

### 8d. Caddy convergence loop (improvement B) ✓

Cleaner design than the original plan: instead of a separate `caddy_desired_state` table, the reconciler derives desired state from the last successful deployment's stored cobaltfile. One column added (`deployments.resolved_cobaltfile`), one reconciler function.

- [x] Migration `0005_deployment_resolved_cobaltfile.sql` — adds the column for storing the cobaltfile that was actually used
- [x] `store.SetResolvedCobaltfile` writes the column; the orchestrator (8c) calls it after Preparer parses cobalt.json
- [x] `worker.ReconcileCaddyState` walks every project that has a last-successful deployment, derives expected upstream from the stored cobaltfile, GETs Caddy current state, PATCHes if divergent
- [x] Handles route-missing case (Caddy wiped → recreate via `AddProjectRoute` + `ServeService`/`ServeStaticSite`)
- [x] Handles upstream drift (correct project route, wrong upstream → `ServeService` to fix)
- [x] Handles domain drift (calls `SetDomainsForProject` every cycle — high-level reconcile is idempotent)
- [x] Per-project failures logged and skipped (one bad cobaltfile doesn't halt the sweep)
- [x] Pre-§8d deployments without `resolved_cobaltfile` skipped — next deploy populates them
- [x] Cron / no-web projects produce no Caddy state changes
- [x] Tests: 11 covering in-sync no-op, drift correction, missing-route recreate, static-site recreate via `ServeStaticSite`, static-site drift skipped (no cheap probe), no-last-success skip, missing-resolved-cobaltfile skip, no-domains skip, no-web-service skip, per-project error continues sweep, list-projects error bubbles
- [ ] Wire reconciler into scheduler (`@every 30s`) on daemon startup — lands with §9 wiring

### 8e. Per-deployment log rotation (improvement H) ✓

- [x] `deploy.DeployLogPath` / `OpenDeployLog`: per-deployment append-mode file at `{dataDir}/logs/deployments/{name}/{n}.log`
- [x] Orchestrator `openLog` helper: uses `LogWriter` if set (tests), otherwise opens the file under DataDir, fallback to `io.Discard` if open fails
- [x] `worker.RotateDeployLogs`: per-cycle, gzips `.log` older than 30d, purges `.log.gz` older than 1y, atomic via tmp+rename
- [x] Per-file failures logged + skipped — one bad file doesn't halt
- [x] Tests covering rotate / purge / decompress round-trip / missing-root / defaults
- [ ] API endpoint to stream a deploy's log file (SSE follow with offset) — lands with §9b

## 9. HTTP API + daemon wiring

Splitting into sub-PRs since §9 spans both "infrastructure that finally collaborates" and "the entire CLI surface area".

### 9a. Daemon wiring ✓

- [x] `server.Run` opens store, runs `RecoverOnBoot`, constructs docker / caddy / github clients
- [x] Wires `deploy.Orchestrator` (Preparer + Builder + tokens) and starts `deploy.Dispatcher`
- [x] `worker.Scheduler` registers all 5 periodic tasks: image cleanup `@hourly`, network cleanup `@hourly`, pending-app cleanup `@every 10m`, Caddy reconcile `@every 30s`, deploy log rotation `@daily`
- [x] CLI flag `--caddy-socket` (defaults to `caddy.DefaultSocketPath`)
- [x] Shutdown reverses order: HTTP server → dispatcher → scheduler → store; ctx cancellation propagates everywhere
- [x] Worker `ProjectLister` and `ListProjects` aligned with `*store.DB` (one canonical `Project` struct, removed worker-local mirror)
- [x] Live smoke-tested: daemon boots cleanly, all subsystems log "started", `/healthz` 200, clean shutdown

### 9b. HTTP API (handlers)

Split into 4 sub-PRs per `plans/cobalt-9b-http-api.md`. Bare JSON responses, `{"error": "..."}` for non-2xx. URL conventions: `/api/projects/{name}/...` for project-scoped resources, `/api/deployments/{id}` for deployments by global id, `/webhooks/github` and `/github-apps/{id}/...` public.

#### 9b-i. Core resource CRUD ✓

- [x] `pkg/cobaltapi/{project,env,domain,deployment}.go` — public types
- [x] Handlers in `internal/server/api/{api,projects,env,domains,deployments,router}.go`
- [x] 14 endpoints: projects (list/create/get/rename/delete), env (list/set/delete), domains (list/add/remove), deployments (list/create/get/cancel), scale wired through deploy queue
- [x] Store gaps filled: `GetProjectByID`, env CRUD (`SetEnvVar`, `SetEnvVars` transactional, `GetEnvVar`, `DeleteEnvVar`), `ListDeploymentsForProject`
- [x] Tests: 12 covering full CRUD, validation, redeploy enqueue, limit pagination, error body shape, 401 unauthenticated
- [x] Live verified end-to-end with curl

#### 9b-ii. GitHub webhook + manifest flow ✓

- [x] `POST /webhooks/github` — HMAC-SHA256 signature verify per app's webhook_secret, `X-GitHub-Delivery` dedup with 10-min TTL (improvement F)
- [x] Push events enqueue deploys for every project tracking `(repo, branch)`; branch deletes ignored
- [x] Installation lifecycle (`created`/`deleted`) syncs `github_app_installations`; `installation_repositories` syncs the repo bridge
- [x] Manifest flow: `POST /api/github-apps/create` returns `{id, url, expiresAt}`; `GET /github-apps/{id}/create` serves auto-submitting HTML form to GitHub; `GET /github-apps/{id}/created` validates state (constant-time) + exchanges code + persists app + redirects to install page
- [x] `GET /api/github-apps`, `GET /api/github-app-repos`, `POST /api/github-apps/prune`
- [x] Store gaps: pending_apps CRUD, github-app lookups by app_id and installation_id, repo bridge CRUD, `FindProjectsForRepoBranch`
- [x] CLI flag `--public-host` for daemon's external hostname
- [x] Tests: dedup unit (5 cases incl. TTL expiry); webhook integration (10 cases)

#### 9b-iii-a. SSE streaming ✓

- [x] `internal/server/api/sse.go` — `sseWriter` with data/heartbeat helpers, `X-Accel-Buffering: no` so reverse proxies don't buffer
- [x] `GET /api/deployments/{id}/output` — streams the per-deployment log file from §8e, follows in-flight, tolerates missing file, `?offset=N` for resume, byte-offset `id:` events
- [x] `GET /api/projects/{name}/logs` — shells out to `docker service logs --follow` against the live deployment's web service (`?service=` overrides)
- [x] `internal/server/docker/logs.go` — `Client.ServiceLogs(ctx, name, follow, w)` wrapper
- [x] Tests: 11 covering terminal/follow/missing/offset/service-not-deployed-yet + SSE primitive shape

#### 9b-iii-b. WebSocket `cobalt run` ✓

- [x] `pkg/cobaltapi/run.go` — `RunFrame` envelope (stdin/stdout/stderr/exit), `RunSubprotocol = "cobalt-run.v1"`
- [x] `GET /api/projects/{name}/run` — WebSocket endpoint via `coder/websocket` (active fork of archived nhooyr)
- [x] Container started with `docker.Run` against last successful deployment's image; attached to deployment network + cobalt-main
- [x] Bidirectional streaming: WS read pump → stdin pipe; stdout/stderr pipes → WS frames
- [x] `extraRunParams` from the resolved cobaltfile threaded through (PR #92 spirit — `--add-host host.docker.internal:host-gateway` works)
- [x] Exit frame on container terminate; non-zero error → exit code 1 (real exit code plumbing deferred)
- [x] Goroutine lifecycle: output pumps via WaitGroup, stdin pump leaks until WS close (avoids deadlock from `conn.Read` blocking)
- [x] Query params: `command` (required), `service` (optional, defaults to "web")
- [x] Tests: 7 covering stdin echo, stdout frames, stderr frames, non-zero exit code, missing command (400), no successful deploy (404), `resolveRunImage` defaults / overrides / extraRunParams threading
- [ ] TTY/resize support — deferred (most uses of `cobalt run` are non-interactive)

#### 9b-iv. Volumes / meta / apikeys / invites

- [ ] Volume export / import (binary tar streaming)
- [ ] Meta info / host / upgrade-stub
- [ ] APIKey CRUD + invite create/accept

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
