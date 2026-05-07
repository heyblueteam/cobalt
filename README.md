# Cobalt

**Cobalt** — Deploy anything, anywhere. 

An open-source, self-hostable alternative to Heroku, Netlify, Render, Vercel, and AWS Amplify. Built for Docker Swarm with automatic HTTPS via Caddy.

Powers mission-critical infrastructure for over 19,000 organizations via [Blue](https://blue.cc).

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](https://golang.org/)

## Features

- **Zero-config deployments** — Push to Git, Cobalt handles the rest
- **cobalt.json** — Drop a single config file in your repo and it just works
- **Automatic HTTPS** — Caddy integrates seamlessly, certificates auto-renew
- **Docker Swarm ready** — Single command deploys your stack to Swarm
- **Sidecar database** — Built-in rqlite for HA-ready persistence (no external DB needed)
- **GitHub integration** — Webhook-driven deploys from any GitHub repo
- **Single binary** — One executable, one deployment target

## Quick Start

```bash
# Install
go install github.com/heyblueteam/cobalt/cmd/cobalt@latest

# Start the daemon
cobalt server --data-dir /var/cobalt

# Connect your repo
cobalt init --github-repo owner/repo --branch main

# Deploy
cobalt deploy
```

## Why Cobalt?

Most deployment tools force you into a specific workflow. Cobalt is different:

| Feature | Cobalt | Kubernetes | Docker Compose |
|---------|--------|------------|----------------|
| Single binary | ✅ | ❌ | ✅ |
| No YAML config | ✅ | ❌ | ❌ |
| Auto HTTPS | ✅ | ❌ | ❌ |
| Git push deploy | ✅ | ❌ | ❌ |
| Swarm native | ✅ | ❌ | ❌ |
| Built-in DB | ✅ (rqlite) | ❌ | ❌ |

## How It Works

```
┌─────────────────────────────────────────────────────────────┐
│                         COBALT DAEMON                        │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│   CLI (You)  ──────────►  HTTP API  ──────────►  Deploy     │
│                                                              │
│                            │                    │            │
│                            ▼                    ▼            │
│                      ┌──────────┐        ┌──────────┐        │
│                      │  Store   │        │  Build   │        │
│                      │ (rqlite) │        │  Kit     │        │
│                      └──────────┘        └──────────┘        │
│                            │                    │            │
│                            ▼                    ▼            │
│                      ┌──────────────────────────────┐       │
│                      │        Caddy + Docker        │       │
│                      └──────────────────────────────┘       │
└─────────────────────────────────────────────────────────────┘
```

## Requirements

- Docker (with Swarm mode enabled)
- Go 1.25+ (for development)
- Linux/ARM64 (for production)

## Documentation

Full documentation at [blue.cc/cobalt/docs](https://blue.cc/cobalt/docs).

- [Getting Started](https://blue.cc/cobalt/docs/getting-started)
- [Architecture](https://blue.cc/cobalt/docs/architecture)
- [CLI Reference](https://blue.cc/cobalt/docs/commands)
- [Deployment Guide](https://blue.cc/cobalt/docs/deployment)

## Development

```bash
# Clone
git clone https://github.com/heyblueteam/cobalt.git
cd cobalt

# Build
go build -o cobalt ./cmd/cobalt

# Test
go test ./...

# Run locally
./cobalt server --addr :8080 --data-dir /tmp/cobalt-data
```

## Architecture

Cobalt is structured as a monolithic Go application with clear boundaries:

```
cmd/cobalt/         # CLI commands (cobra)
internal/server/    # Daemon HTTP API
  ├── store/        # Persistence layer (rqlite)
  ├── deploy/       # Deployment engine
  ├── docker/       # Docker client
  └── caddy/        # Caddy admin client
```

## Status

Cobalt powers mission-critical infrastructure for over 19,000 organizations via [Blue](https://blue.cc).

## Contributing

Contributions welcome! Please open an issue or submit a PR on GitHub.

## License

MIT License — see [LICENSE](LICENSE) for details.

---

Built by [Blue](https://blue.cc) | [Docs](https://blue.cc/cobalt/docs)
