# Cobalt

**Cobalt** — Deploy anything, anywhere.

An open-source, self-hostable alternative to Heroku, Netlify, Render, Vercel, and AWS Amplify. Built for Docker Swarm with automatic HTTPS via Caddy.

Powers mission-critical infrastructure for over 19,000 organizations via [Blue](https://blue.cc).

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](https://golang.org/)

## Install

### macOS / Linux (Homebrew)

```bash
brew install heyblueteam/tap/cobalt
```

### Windows (Scoop)

```bash
scoop bucket add heyblueteam https://github.com/heyblueteam/scoop-bucket
scoop install cobalt
```

### Docker

```bash
# Pull the latest image
docker pull ghcr.io/heyblueteam/cobalt:latest

# Run with Docker Compose (recommended)
curl -sL https://raw.githubusercontent.com/heyblueteam/cobalt/main/deploy/compose/docker-compose.yml | docker compose -f - up -d
```

### Binary Downloads

Download the latest release for your platform from the [GitHub Releases](https://github.com/heyblueteam/cobalt/releases) page:

| OS | Architecture | Download |
|----|--------------|----------|
| macOS | Apple Silicon | `cobalt_vX.X.X_darwin_arm64.tar.gz` |
| macOS | Intel | `cobalt_vX.X.X_darwin_amd64.tar.gz` |
| Linux | ARM64 | `cobalt_vX.X.X_linux_arm64.tar.gz` |
| Linux | AMD64 | `cobalt_vX.X.X_linux_amd64.tar.gz` |
| Windows | AMD64 | `cobalt_vX.X.X_windows_amd64.zip` |

Extract and move to your PATH:

```bash
tar -xzf cobalt_*.tar.gz
sudo mv cobalt /usr/local/bin/
```

### Build from Source

```bash
go install github.com/heyblueteam/cobalt/cmd/cobalt@latest
```

## Quick Start

### 1. Initialize a server

```bash
cobalt init user@your-server.com
```

This SSHs into your server, deploys the Cobalt stack via Docker Compose, and configures your local CLI.

### 2. Deploy your first project

```bash
# Add a project
cobalt projects add myapp --github-repo owner/repo

# Configure environment
# Each var is exposed at /run/secrets/KEY during build, plus an aggregate
# /run/secrets/.env containing every var (drop-in for disco-style Dockerfiles).
cobalt env set DATABASE_URL=postgres://...

# Deploy
cobalt deploy
```

For Dockerfile builds, Cobalt also provides the exact checked-out revision as
the public `COBALT_COMMIT` build argument. Declare it only where the build needs
source identity:

```dockerfile
ARG COBALT_COMMIT
RUN echo "Building ${COBALT_COMMIT}"
```

### 3. View your deployment

```bash
# List deployments
cobalt deployments list

# Stream logs
cobalt logs -f

# Live resource dashboard (host + per-container)
cobalt stats
```

## Features

- **Zero-config deployments** — Push to Git, Cobalt handles the rest
- **cobalt.json** — Drop a single config file in your repo and it just works
- **Automatic HTTPS** — Caddy integrates seamlessly, certificates auto-renew
- **Docker Swarm ready** — Single command deploys your stack to Swarm
- **Sidecar database** — Built-in rqlite for HA-ready persistence (no external DB needed)
- **GitHub integration** — Webhook-driven deploys from any GitHub repo
- **Live monitoring** — `cobalt stats` shows host + per-container CPU/memory grouped by project, from your laptop
- **Single binary** — One executable, one deployment target

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

Full documentation coming soon.

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

Built by [Blue](https://blue.cc)
