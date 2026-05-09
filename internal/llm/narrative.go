package llm

const NARRATIVE = `# Cobalt

Cobalt is a self-hosted deployment platform for Docker Swarm with automatic HTTPS via Caddy. It deploys your applications with a single command — no YAML configuration, no Kubernetes required.

Powers mission-critical infrastructure for over 19,000 organizations via Blue (https://blue.cc).

## Features

- Zero-config deployments — Push to Git, Cobalt handles the rest
- cobalt.json — Drop a single config file in your repo and it just works
- Automatic HTTPS — Caddy integrates seamlessly, certificates auto-renew
- Docker Swarm ready — Single command deploys your stack to Swarm
- Sidecar database — Built-in rqlite for HA-ready persistence (no external DB needed)
- GitHub integration — Webhook-driven deploys from any GitHub repo
- Single binary — One executable, one deployment target

## Prerequisites

- Docker (with Swarm mode enabled)
- A server running Linux (ARM64 or AMD64)
- GitHub account (for repo access)

## Installation

Install the CLI on your development machine:

` + "```" + `bash
# macOS/Linux
brew install heyblueteam/tap/cobalt

# or download from releases
curl -fsSL https://github.com/heyblueteam/cobalt/releases/latest/download/cobalt-linux-arm64 -o cobalt
chmod +x cobalt
` + "```" + `

## Quick Start

1. SSH into your server and start the daemon:

` + "```" + `bash
cobalt server --data-dir /var/cobalt
` + "```" + `

2. On your local machine, initialize the CLI:

` + "```" + `bash
cobalt init user@your-server.com
` + "```" + `

3. Connect your GitHub repo:

` + "```" + `bash
cobalt github:apps:add
cobalt projects:add --name myapp --github owner/repo --domain app.example.com
` + "```" + `

4. Deploy by pushing to Git:

` + "```" + `bash
git push
` + "```" + `

## Configuration

Cobalt uses a single config file called cobalt.json in your project root:

` + "```" + `json
{
  "name": "myapp",
  "github": "owner/repo",
  "build": {
    "dockerfile": "Dockerfile"
  }
}
` + "```" + `

## CLI Commands

### Server Management
- cobalt server — Start the daemon
- cobalt servers — List connected servers
- cobalt use — Switch active server

### Projects
- cobalt projects — List projects
- cobalt projects:add — Create a new project
- cobalt projects:remove — Remove a project

### Deployments
- cobalt deploy — Trigger a new deployment
- cobalt deployments — List deployments
- cobalt deployments:watch — Watch deployment logs

### Environment Variables
- cobalt env — List environment variables
- cobalt env:set KEY=value — Set an environment variable
- cobalt env:unset KEY — Remove an environment variable

At build time each var is mounted as a buildkit secret at /run/secrets/KEY
(opt in via RUN --mount=type=secret,id=KEY ...), and an aggregate of all
vars is mounted at /run/secrets/.env in dotenv form (drop-in for disco-style
Dockerfiles using RUN --mount=type=secret,id=.env cp /run/secrets/.env .env).

### Domains
- cobalt domains — List custom domains
- cobalt domains:add domain.com — Add a custom domain

### Logs
- cobalt logs — Stream application logs
- cobalt logs:build — View build logs

### Run
- cobalt run — Run a one-off command in the deployment environment

### Scale
- cobalt scale — Scale service replicas

### Volumes
- cobalt volumes — List volumes

### GitHub
- cobalt github:apps — Manage GitHub Apps
- cobalt github:repos — List authorized repositories

### API Keys
- cobalt apikeys — Manage API keys

### Meta
- cobalt meta info — Show daemon version and uptime
- cobalt meta host — Update daemon's public hostname
- cobalt meta upgrade — Upgrade the daemon

## Contact

Built by Blue (https://blue.cc)
Docs: https://blue.cc/cobalt/docs
`
