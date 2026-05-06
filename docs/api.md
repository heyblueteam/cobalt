# Cobalt API Reference

Base URL: `https://<host>/api`

All endpoints except `/healthz` and `/webhooks/github` require a `Bearer <apikey>`
header. Keys are managed via the `apikeys` endpoints.

Error responses use `{"error": "<message>"}` with an appropriate HTTP status code.

---

## Health

### `GET /healthz`

Unauthenticated health check.

**Response** `200`
```json
{"status": "ok"}
```

---

## Projects

### `GET /api/projects`

List all projects.

**Response** `200`
```json
[{
  "id": 1,
  "name": "api",
  "githubRepo": "heyblueteam/api",
  "branch": "main",
  "githubAppInstallationId": 42,
  "createdAt": 1700000000,
  "updatedAt": 1700000001
}]
```

**CLI**: `cobalt projects list`

---

### `POST /api/projects`

Create a new project.

**Request**
```json
{
  "name": "api",
  "githubRepo": "heyblueteam/api",
  "branch": "main",
  "domain": "api.blue.cc"
}
```

**Response** `201`
```json
{
  "id": 1,
  "name": "api",
  "githubRepo": "heyblueteam/api",
  "branch": "main",
  "createdAt": 1700000000,
  "updatedAt": 1700000000
}
```

**CLI**: `cobalt projects add <name> --github owner/repo [--branch main] [--domain ...]`

---

### `GET /api/projects/{name}`

Get a project by name.

**CLI**: `cobalt projects list`

---

### `PATCH /api/projects/{name}`

Rename a project.

**Request**
```json
{"name": "api-v2"}
```

**Response** `200` — Project object with updated name.

**CLI**: `cobalt projects rename <old> <new>`

---

### `DELETE /api/projects/{name}`

Delete a project. Also removes associated domains, env vars, and deployments.

**Response** `204 No Content`

**CLI**: `cobalt projects remove <name>`

---

## Environment Variables

### `GET /api/projects/{name}/env`

List env vars for a project.

**Response** `200`
```json
[
  {"key": "NODE_ENV", "value": "production"},
  {"key": "DATABASE_URL", "value": "postgres://..."}
]
```

**CLI**: `cobalt env list`

---

### `POST /api/projects/{name}/env`

Set (upsert) env vars. Setting a key that already exists overwrites it.

**Request**
```json
{
  "vars": {
    "NODE_ENV": "production",
    "DATABASE_URL": "postgres://..."
  },
  "redeploy": false
}
```

Set `"redeploy": true` to automatically enqueue a new deployment after the env change.

**Response** `200` — Updated list of all env vars.

**CLI**: `cobalt env set KEY=VAL [KEY2=VAL2 ...] [--redeploy]`

---

### `DELETE /api/projects/{name}/env/{key}`

Remove an env var.

**Response** `204 No Content`

**CLI**: `cobalt env remove <KEY>`

---

## Domains

### `GET /api/projects/{name}/domains`

List domains for a project.

**Response** `200`
```json
[
  {"name": "api.blue.cc", "createdAt": 1700000000}
]
```

**CLI**: `cobalt domains list`

---

### `POST /api/projects/{name}/domains`

Add a domain. Provisions a TLS certificate via Caddy synchronously.

**Request**
```json
{"name": "api.blue.cc"}
```

**Response** `201`
```json
{"name": "api.blue.cc", "createdAt": 1700000000}
```

**CLI**: `cobalt domains add <domain>`

---

### `DELETE /api/projects/{name}/domains/{domain}`

Remove a domain. Cleans up the Caddy config for the removed domain.

**Response** `204 No Content`

**CLI**: `cobalt domains remove <domain>`

---

## Deployments

### `GET /api/projects/{name}/deployments`

List deployments for a project. Accepts optional `?limit=N` query param (default 20).

**Response** `200`
```json
[{
  "id": 42,
  "projectId": 1,
  "number": 5,
  "status": "success",
  "commitSha": "a1b2c3d4",
  "noCache": false,
  "createdAt": 1700000000,
  "startedAt": 1700000001,
  "finishedAt": 1700000120
}]
```

**CLI**: `cobalt deployments list [--limit N]`

---

### `POST /api/projects/{name}/deployments`

Enqueue a new deployment.

**Request**
```json
{
  "commit": "a1b2c3d4",
  "noCache": false,
  "cobaltfileOverride": "{...}"
}
```

- `commit` — SHA to deploy (optional, defaults to latest on branch).
- `noCache` — disable Docker build cache.
- `cobaltfileOverride` — raw JSON string to use instead of the repo's `cobalt.json`.

**Response** `201`
```json
{
  "id": 42,
  "projectId": 1,
  "number": 5,
  "status": "queued",
  "commitSha": "a1b2c3d4",
  "noCache": false,
  "createdAt": 1700000000
}
```

**CLI**: `cobalt deploy [--commit <sha>] [--no-cache] [--file <path>] [--no-follow]`

---

### `GET /api/deployments/{id}`

Get a single deployment by ID.

**CLI**: `cobalt deployments list`

---

### `POST /api/deployments/{id}/cancel`

Cancel an in-flight deployment. Does nothing if the deployment is already terminal.

**Response** `200 OK`

**CLI**: `cobalt deployments cancel [--deployment <id>]`

---

### `GET /api/deployments/{id}/output`

Stream deployment output as Server-Sent Events. Accepts optional `?offset=N` for
reconnect resumes.

**Response** `200 text/event-stream`

```
id: 1024
data: Building image api-5-web...

id: 2048
data: Starting service api-5-web...
```

The stream closes when the deployment reaches a terminal state. Heartbeat comments
are sent every 15s while the deployment is in flight.

**CLI**: `cobalt deploy` (follow mode), `cobalt deployments output [--deployment <id>]`

---

## Logs

### `GET /api/projects/{name}/logs`

Stream service logs as Server-Sent Events. Accepts optional `?service=NAME` query
param (defaults to `"web"`). Reads from the most recent successful deployment.

**Response** `200 text/event-stream`

```
data: [web.1] Listening on :3000

data: [web.1] GET /api/health 200
```

**CLI**: `cobalt logs [--service <s>]`

---

## Run

### `GET /api/projects/{name}/run`

Open a WebSocket for one-off command execution. Accepts query params:

- `command` (required) — the command to run.
- `service` (optional, default `"web"`) — which service config to use for image/network.

**WebSocket subprotocol**: `cobalt-run.v1`

Frames are JSON-encoded:

```json
{"type": "stdin", "data": "ls -la\n"}
```
```json
{"type": "stdout", "data": "file1\nfile2\n"}
```
```json
{"type": "stderr", "data": "warning message"}
```
```json
{"type": "exit", "code": 0}
```

**CLI**: `cobalt run "<command>" [--service <s>]`

---

## Volumes

### `GET /api/projects/{name}/volumes`

List named volumes for a project.

**Response** `200`
```json
[
  {"name": "data", "fullName": "cobalt-volume-7-data"},
  {"name": "uploads", "fullName": "cobalt-volume-7-uploads"}
]
```

**CLI**: `cobalt volumes list`

---

### `POST /api/projects/{name}/volumes/{volume}/export`

Export a volume as a gzipped tar. Returns binary data.

**Response** `200 application/octet-stream` — gzipped tar of volume contents.

**CLI**: `cobalt volumes export --volume <name> [--output <path>] [--force]`

---

### `POST /api/projects/{name}/volumes/{volume}/import`

Import data into a volume. Accepts raw binary (gzipped tar) as the request body.

**Request** `POST` with `Content-Type: application/octet-stream` and gzipped tar body.

**Response** `200 OK`

**CLI**: `cobalt volumes import --volume <name> [--input <path>]`

---

## API Keys

### `GET /api/apikeys`

List API keys (without raw key values — those are only returned at creation time).

**Response** `200`
```json
[
  {"id": 1, "name": "prod-key", "createdAt": 1700000000, "lastUsedAt": 1700000900}
]
```

**CLI**: `cobalt apikeys list`

---

### `POST /api/apikeys`

Create a new API key. The raw key is returned **once** — cobalt stores only the
SHA-256 hash. Save it immediately.

**Request**
```json
{"name": "prod-key"}
```

**Response** `201`
```json
{
  "id": 1,
  "name": "prod-key",
  "key": "sk-abc123xyz...",
  "createdAt": 1700000000
}
```

**CLI**: `cobalt apikeys create <name>`

---

### `DELETE /api/apikeys/{id}`

Delete an API key. Subsequent requests using this key will return 401.

**Response** `204 No Content`

**CLI**: `cobalt apikeys remove <id>`

---

## GitHub Apps

### `GET /api/github-apps`

List registered GitHub Apps.

**Response** `200`
```json
[{
  "id": 1,
  "appId": 12345,
  "slug": "cobalt-ci",
  "name": "Cobalt CI",
  "owner": "heyblueteam",
  "htmlUrl": "https://github.com/apps/cobalt-ci",
  "createdAt": 1700000000
}]
```

**CLI**: `cobalt github apps list`

---

### `GET /api/github-app-repos`

List repos accessible to cobalt's GitHub App installations.

**Response** `200`
```json
[{
  "id": 1,
  "installationId": 100,
  "repoId": 123456789,
  "fullName": "heyblueteam/api",
  "private": false,
  "defaultBranch": "main"
}]
```

**CLI**: `cobalt github repos list`

---

### `POST /api/github-apps/create`

Start GitHub App registration. Returns a URL that the user opens to install the App.

**Request**
```json
{"organization": "heyblueteam"}
```

**Response** `201`
```json
{
  "id": 1,
  "url": "https://github.com/apps/cobalt/installations/new?state=abc123",
  "expiresAt": 1700001800
}
```

**CLI**: `cobalt github apps add --organization <org> [--non-interactive]`

---

### `POST /api/github-apps/prune`

Remove stale GitHub Apps and sync repo list with GitHub's current state.

**Response** `200`
```json
{
  "appsRemoved": 1,
  "installationsRemoved": 2,
  "reposAdded": 10,
  "reposRemoved": 3
}
```

**CLI**: `cobalt github apps prune`

---

## Meta

### `GET /api/meta/info`

Daemon information. Useful for incident response and diagnostics.

**Response** `200`
```json
{
  "version": "0.1.0",
  "hostname": "cobalt-host",
  "uptimeSecs": 86400,
  "startedAt": 1700000000
}
```

**CLI**: `cobalt meta info`

---

## Webhooks (unauthenticated)

### `POST /webhooks/github`

GitHub webhook receiver. Verifies HMAC-SHA256 signatures per-app. Processes
push events (enqueues deploy) and installation events (syncs repos).

### `GET /github-apps/{id}/create`

Manifest registration form. Returns an auto-submitting HTML page that POSTs to
GitHub's App manifest flow.

### `GET /github-apps/{id}/created`

Manifest callback. Exchanges the temporary code for a permanent App installation.
