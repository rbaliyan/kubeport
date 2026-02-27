# kubeport

[![CI](https://github.com/rbaliyan/kubeport/actions/workflows/ci.yml/badge.svg)](https://github.com/rbaliyan/kubeport/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/rbaliyan/kubeport)](https://github.com/rbaliyan/kubeport/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/rbaliyan/kubeport)](https://goreportcard.com/report/github.com/rbaliyan/kubeport)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](https://opensource.org/licenses/MIT)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/rbaliyan/kubeport/badge)](https://scorecard.dev/viewer/?uri=github.com/rbaliyan/kubeport)

**The persistent, configuration-driven proxy for Kubernetes developers.** Stop restarting `kubectl port-forward`. Start coding.

---

## Why Kubeport?

Standard port-forwarding is brittle. Kubeport transforms flaky tunnels into a stable, automated local development gateway.

- **Self-Healing** — Dropped connections? Kubeport detects and restarts them instantly with exponential backoff.
- **Config-as-Code** — Define all your services in one `kubeport.yaml` and check it into your repo for the whole team.
- **Lifecycle Hooks** — Run shell commands, exec binaries, or fire webhooks when tunnels connect, disconnect, or fail.
- **Multi-Port Discovery** — Forward every port a service exposes with `ports: all`, or pick specific named ports.
- **Background Daemon** — Runs quietly in the background; control everything with simple CLI commands.
- **Zero Cluster Footprint** — Pure client-side SPDY tunnels. Nothing deployed to your cluster.

## Quick Start

```bash
# Install
brew install rbaliyan/tap/kubeport

# Create a config
kubeport config init

# Start all services
kubeport start

# Check what's running
kubeport status
```

```
$ kubeport status
  SERVICE           STATUS       LOCAL     REMOTE    POD
  My API            connected    :8080  →  :80       my-api-7d4b8c6f9-x2k4m
  Redis             connected    :6379  →  :6379     redis-0
  Platform/http     connected    :80    →  :80       platform-7f8a9b-q4r2s
  Platform/grpc     connected    :9090  →  :9090     platform-7f8a9b-q4r2s
```

## The `kubeport.yaml`

Don't make your teammates guess which ports to forward. Check this into your project root:

```yaml
# kubeport.yaml
context: my-cluster
namespace: default

services:
  - name: Auth API
    service: auth-api
    local_port: 8080
    remote_port: 8080

  - name: Postgres
    pod: postgres-0
    local_port: 5432
    remote_port: 5432
    namespace: databases

  - name: Platform
    service: platform-svc
    ports: all                  # auto-discover and forward every port

supervisor:
  health_check_interval: 10s   # TCP probe frequency
  health_check_threshold: 3    # consecutive failures before restart
  max_restarts: 0              # 0 = unlimited

hooks:
  - name: notify
    type: shell
    shell:
      forward_connected: notify-send "kubeport" "${KUBEPORT_SERVICE} ready on port ${KUBEPORT_LOCAL_PORT}"
      forward_failed: notify-send -u critical "kubeport" "${KUBEPORT_SERVICE} failed: ${KUBEPORT_ERROR}"
```

Both YAML and TOML formats are supported. See [example.yaml](example.yaml) and [example.toml](example.toml).

## Feature Deep Dive

### Self-Healing Connections

Each port-forward runs in its own supervised goroutine with:

- **TCP health checks** every 10s (configurable) to detect silent failures
- **Automatic restart** with exponential backoff (1s → 30s) and 25% jitter
- **Backoff reset** if a connection stays healthy for 30+ seconds
- **Smart pod selection** — prefers Ready pods, skips terminating pods during rollouts

### Multi-Port Auto-Discovery

Don't list every port manually. Let kubeport discover them from the Kubernetes API:

```yaml
services:
  # Forward all ports
  - name: Platform
    service: platform-svc
    ports: all

  # Pick specific named ports with local overrides
  - name: Backend
    service: backend-svc
    ports:
      - name: http
        local_port: 8080
      - name: grpc

  # All ports except metrics, shifted by an offset
  - name: Infra
    service: infra-svc
    ports: all
    exclude_ports: [metrics]
    local_port_offset: 10000
```

Each discovered port becomes an independent supervised forward (`Platform/http`, `Platform/grpc`) with its own health checks and restart tracking.

### Lifecycle Hooks

Kubeport isn't just a tunnel — it's a workflow engine. Use hooks to bridge the gap between your cluster and your local machine.

| Event | When It Fires |
|-------|---------------|
| `manager_starting` | Before any forwards begin (gate event — can block startup) |
| `manager_stopped` | All forwards stopped, cleanup complete |
| `forward_connected` | A tunnel is ready and healthy |
| `forward_disconnected` | A tunnel dropped (will retry) |
| `forward_failed` | Max restarts exceeded — permanently failed |
| `forward_stopped` | A forward was intentionally stopped |
| `health_check_failed` | A single health-check probe failed |
| `service_added` | A service was dynamically added |
| `service_removed` | A service was dynamically removed |

Three hook types: **shell** (`sh -c`), **exec** (direct binary), and **webhook** (HTTP POST).

**Gate startup on VPN:**

```yaml
hooks:
  - name: vpn-check
    type: shell
    events: [manager_starting]
    fail_mode: closed              # block startup if VPN is down
    shell:
      manager_starting: ./scripts/ensure-vpn.sh
```

**Slack alerts on failures:**

```yaml
hooks:
  - name: slack
    type: webhook
    events: [forward_failed]
    webhook:
      url: https://hooks.slack.com/services/T.../B.../xxx
      body_template: '{"text": ":warning: ${SERVICE} failed: ${ERROR}"}'
```

See the [hooks guide](docs/hooks.md) for all options, environment variables, and more examples.

### Dynamic Service Management

Add, remove, and reload services without restarting the daemon:

```bash
# Add a service on the fly (--persist writes it to the config file)
kubeport add "Postgres" svc/postgres:5432:5432 --persist

# Remove a running service
kubeport remove "Postgres"

# Reload after editing the config file
kubeport reload

# Merge services from another file
kubeport apply overlay.yaml
```

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

```bash
kubeport start              # Start daemon in background
kubeport status             # Check all forwards
kubeport status --json      # Machine-readable output
kubeport fg                 # Run in foreground (for debugging or containers)
kubeport stop               # Graceful shutdown
kubeport restart            # Stop + start
kubeport logs               # Follow daemon logs
kubeport reload             # Sync config changes to running daemon
```

### Config Management

```bash
kubeport config init        # Create a starter config (YAML or TOML)
kubeport config show        # Display current config in a table
kubeport config validate    # Validate config file
kubeport config set context my-cluster
kubeport config add --name "Redis" --pod redis-0 --local-port 6379 --remote-port 6379
kubeport config remove "Redis"
kubeport config path        # Print resolved config file path
```

## Comparison

| Capability | kubectl | Telepresence | Kubeport |
|---|---|---|---|
| Auto-reconnect | | x | x |
| Health checks | | | x |
| Config file | | | x |
| Multi-port discovery | | | x |
| Lifecycle hooks | | x | x |
| Dynamic add/remove | | | x |
| Background daemon | | x | x |
| Zero cluster footprint | x | | x |

## Install

```bash
# Homebrew
brew install rbaliyan/tap/kubeport

# Install script
curl -sSfL https://raw.githubusercontent.com/rbaliyan/kubeport/main/install.sh | sh

# Go
go install github.com/rbaliyan/kubeport@latest
```

Pre-built binaries for Linux and macOS (amd64/arm64) are available on the [releases page](https://github.com/rbaliyan/kubeport/releases). Shell completions for bash, zsh, and fish are included. See the [installation guide](docs/installation.md) for all options.

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
