# Roadmap

Planned features not yet implemented.

## High priority

- Managed Postgres addon — `cobalt postgres create`, auto-injected `DATABASE_URL`, attach/detach to projects, local tunnel for psql
- Automated backups — project volumes + Postgres → S3/R2/B2 on a schedule, one-command restore
- Preview environments per PR — ephemeral deployments per GitHub PR, torn down on close
- Redis addon — same pattern as Postgres
- Team / RBAC — multiple API keys with scoped permissions

## Other

- Tunnels (reverse tunnels for local dev)
- Syslog forwarding
- Multi-node Swarm (`nodes add/remove/list`) — cobalt is single-node
- Private registries (`login`/`set`/`unset`)
- CORS origins management
- API key invites flow
- Project key/value store
- DNS helpers + random project name generator
- Cron run history persisted in rqlite (visible via `cobalt crons history`)
- Cron `concurrencyPolicy: forbid` to skip a fire when the previous one is still running
- Per-project pause-all-crons flag for maintenance windows
