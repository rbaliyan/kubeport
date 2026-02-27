# kubeport

[![CI](https://github.com/rbaliyan/kubeport/actions/workflows/ci.yml/badge.svg)](https://github.com/rbaliyan/kubeport/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/rbaliyan/kubeport)](https://github.com/rbaliyan/kubeport/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/rbaliyan/kubeport)](https://goreportcard.com/report/github.com/rbaliyan/kubeport)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](https://opensource.org/licenses/MIT)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/rbaliyan/kubeport/badge)](https://scorecard.dev/viewer/?uri=github.com/rbaliyan/kubeport)

**Port-forward to Kubernetes services that just works.** kubeport keeps your connections alive, restarts them when they drop, and gets out of your way.

---

## The Problem

If you develop against a Kubernetes cluster, you know the pain:

- `kubectl port-forward` dies silently and you don't notice for 20 minutes
- You have to juggle multiple terminal windows for multiple services
- You restart everything manually after your laptop wakes from sleep
- There's no visibility into what's connected and what's broken

## The Solution

kubeport is a single binary that manages all your port-forwards in the background. Define your services once in a config file, run `kubeport start`, and forget about it.

```
$ kubeport status
  SERVICE         STATUS       LOCAL     REMOTE    POD
  My API          connected    :8080  →  :80       my-api-7d4b8c6f9-x2k4m
  Redis           connected    :6379  →  :6379     redis-0
  Vault           connected    :8200  →  :8200     vault-0
```

When a connection drops, kubeport detects it within seconds and reconnects automatically — with exponential backoff so it doesn't hammer your cluster.

## Key Features

- **Self-healing connections** — Health checks detect failures and restart forwards automatically with exponential backoff
- **One config, many services** — Define all your forwards in a single YAML or TOML file
- **Background daemon** — Runs as a background process; control it with simple CLI commands
- **No kubectl required** — Uses the Kubernetes Go client directly; kubeport is a single self-contained binary
- **Lifecycle hooks** — Run shell commands, exec binaries, or fire webhooks on connect/disconnect/failure events
- **Multi-port auto-discovery** — Forward all ports from a service with `ports: all`, or pick specific named ports
- **Dynamic ports** — Set `local_port: 0` and let the OS pick an available port
- **Per-service namespaces** — Mix services from different namespaces in one config

## Install

```bash
# Homebrew
brew install rbaliyan/tap/kubeport

# Install script
curl -sSfL https://raw.githubusercontent.com/rbaliyan/kubeport/main/install.sh | sh

# Go
go install github.com/rbaliyan/kubeport@latest
```

Pre-built binaries are available on the [releases page](https://github.com/rbaliyan/kubeport/releases). See the [installation guide](docs/installation.md) for all options.

## Quick Start

**1. Create a config file:**

```bash
kubeport config init
```

**2. Add your services:**

```bash
kubeport config add --name "My API" --service my-api --local-port 8080 --remote-port 80
kubeport config add --name "Redis" --pod redis-0 --local-port 6379 --remote-port 6379
```

Or write the config directly (`kubeport.yaml`):

```yaml
context: my-cluster
namespace: default
services:
  - name: My API
    service: my-api
    local_port: 8080
    remote_port: 80
  - name: Redis
    pod: redis-0
    local_port: 6379
    remote_port: 6379
  - name: Platform
    service: platform-svc
    ports: all              # forward every port the service exposes
```

**3. Start:**

```bash
kubeport start
```

**4. Check status:**

```bash
kubeport status
```

That's it. Your forwards are running in the background, health-checked, and will auto-restart if anything goes wrong.

### No Config File? No Problem

Run entirely from the command line:

```bash
kubeport start --no-config \
  --context my-cluster \
  --svc "api:svc/my-api:80:8080" \
  --svc "redis:pod/redis-0:6379:6379" \
  --svc "platform:svc/platform-svc:all"
```

## Common Workflows

### Stop and restart

```bash
kubeport stop
kubeport restart
```

### Run in the foreground (for debugging or containers)

```bash
kubeport fg
```

### Add/remove services on the fly

```bash
kubeport add "Postgres" svc/postgres:5432:5432
kubeport remove "Postgres"
```

### Reload after editing config

```bash
kubeport reload
```

### Get notified when things break

```yaml
hooks:
  - name: alert
    type: shell
    shell:
      forward_connected: notify-send "kubeport" "${KUBEPORT_SERVICE} ready"
      forward_failed: notify-send -u critical "kubeport" "${KUBEPORT_SERVICE} failed"
```

See the [hooks guide](docs/hooks.md) for Slack webhooks, VPN gating, syslog integration, and more.

## Documentation

| Guide | Description |
|-------|-------------|
| [Installation](docs/installation.md) | All installation methods, requirements, and shell completions |
| [Configuration](docs/configuration.md) | Config file format, service definitions, supervisor tuning |
| [CLI Reference](docs/cli.md) | Every command and flag |
| [Lifecycle Hooks](docs/hooks.md) | Shell, exec, and webhook hooks with real-world examples |
| [Architecture](docs/architecture.md) | How kubeport works under the hood |
| [Shell Completions](docs/shell-completions.md) | Tab completion for bash, zsh, and fish |

Example config files: [YAML](example.yaml) | [TOML](example.toml)

## Contributing

Contributions are welcome! Here's how to get started:

1. Fork the repository
2. Create a feature branch (`git checkout -b my-feature`)
3. Make your changes
4. Run the tests: `just test`
5. Run the linter: `just lint`
6. Commit and push your branch
7. Open a pull request

### Development Setup

This project uses [mise](https://mise.jdx.dev) for tool management and [just](https://github.com/casey/just) as a command runner.

```bash
mise install       # Install Go, linters, protoc, etc.
just build         # Build the binary
just test          # Run tests
just lint          # Run linter
just fmt           # Format code
```

See the [justfile](justfile) for all available recipes.

## License

[MIT](LICENSE)
