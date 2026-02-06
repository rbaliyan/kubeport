# kubeport

[![CI](https://github.com/rbaliyan/kubeport/actions/workflows/ci.yml/badge.svg)](https://github.com/rbaliyan/kubeport/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/rbaliyan/kubeport)](https://goreportcard.com/report/github.com/rbaliyan/kubeport)

A Kubernetes port-forward supervisor with health checks, automatic restarts, and lifecycle hooks.

## Features

- **Persistent port-forwarding** - Automatically restarts failed connections
- **Health checks** - Periodic connectivity probes with exponential backoff
- **Multiple services** - Forward multiple services/pods in a single config
- **Background daemon** - Run as a background process with gRPC control
- **Lifecycle hooks** - Execute shell commands or webhooks on events
- **Dynamic ports** - Use port 0 for automatic local port assignment
- **Config formats** - YAML and TOML configuration support

## Installation

**Install script:**
```bash
curl -sSfL https://raw.githubusercontent.com/rbaliyan/kubeport/main/install.sh | sh
```

**Go install:**
```bash
go install github.com/rbaliyan/kubeport@latest
```

**Download binary:**

Download from [GitHub Releases](https://github.com/rbaliyan/kubeport/releases) for your platform.

## Quick Start

1. **Create a configuration file:**

```bash
kubeport config init
```

2. **Add services to forward:**

```bash
kubeport config add --name "My API" --service my-api --local-port 8080 --remote-port 80
kubeport config add --name "Redis" --pod redis-0 --local-port 6379 --remote-port 6379
```

3. **Start the proxy:**

```bash
kubeport start
```

4. **Check status:**

```bash
kubeport status
```

## Commands

```bash
kubeport start      # Start proxy in background
kubeport stop       # Stop running proxy
kubeport status     # Show proxy and port status
kubeport logs       # Follow proxy logs
kubeport restart    # Restart proxy
kubeport fg         # Run in foreground (blocking)
kubeport config     # Configuration management
kubeport version    # Show version
kubeport help       # Show help
```

### Config Subcommands

```bash
kubeport config init              # Create new config file
kubeport config show              # Display configuration
kubeport config set context dev   # Set Kubernetes context
kubeport config set namespace app # Set default namespace
kubeport config add [options]     # Add service to forward
kubeport config remove <name>     # Remove service
kubeport config path              # Print config file path
```

## Configuration

### YAML Format

```yaml
context: my-cluster-context
namespace: default
services:
  - name: My API
    service: my-api-service
    local_port: 8080
    remote_port: 80

  - name: Redis Node 0
    pod: redis-node-0
    local_port: 6379
    remote_port: 6379

  - name: Vault
    service: vault
    local_port: 8200
    remote_port: 8200
    namespace: vault  # Override namespace

  - name: Debug Server
    service: debug-svc
    local_port: 0     # Dynamic port assignment
    remote_port: 9090
```

### TOML Format

```toml
context = "my-cluster-context"
namespace = "default"

[[services]]
name = "My API"
service = "my-api-service"
local_port = 8080
remote_port = 80

[[services]]
name = "Redis"
pod = "redis-0"
local_port = 6379
remote_port = 6379
```

### Config Discovery

kubeport searches for configuration in this order:

1. `--config` flag value
2. `kubeport.{yaml,yml,toml}` in current directory
3. `~/.config/kubeport/kubeport.{yaml,yml,toml}`
4. `~/.kubeport/kubeport.{yaml,yml,toml}`

### Environment Variables

| Variable | Description |
|----------|-------------|
| `K8S_CONTEXT` | Override Kubernetes context |
| `K8S_NAMESPACE` | Override default namespace |

## Lifecycle Hooks

Execute actions on port-forward events:

```yaml
hooks:
  pre_start:
    - shell: echo "Starting port-forwards..."

  post_start:
    - shell: notify-send "kubeport" "Port forwards ready"
      services: [My API]  # Only for specific services

  on_error:
    - webhook:
        url: https://hooks.slack.com/...
        method: POST
        body: '{"text": "Port forward failed: {{.Error}}"}'

  shutdown:
    - shell: echo "Shutting down..."
```

### Hook Types

- **shell** - Execute shell command
- **exec** - Execute binary directly
- **webhook** - HTTP request

### Hook Options

```yaml
hooks:
  on_error:
    - shell: ./scripts/alert.sh
      timeout: 10s        # Execution timeout
      fail_mode: ignore   # ignore, warn, or fail
      services: [API]     # Filter by service name
```

## Shell Completions

Shell completions are included in release archives:

**Bash:**
```bash
source completions/kubeport.bash
# Or copy to /etc/bash_completion.d/kubeport
```

**Zsh:**
```bash
source completions/kubeport.zsh
# Or copy to ~/.zsh/completions/_kubeport
```

**Fish:**
```bash
source completions/kubeport.fish
# Or copy to ~/.config/fish/completions/kubeport.fish
```

## How It Works

1. **Start** - kubeport daemonizes itself and starts a gRPC server on a Unix socket
2. **Connect** - For each configured service, it creates a port-forward using kubectl logic
3. **Monitor** - Health checks run every 10 seconds to verify connectivity
4. **Recover** - Failed connections are restarted with exponential backoff (1s to 30s)
5. **Control** - CLI commands communicate with the daemon via gRPC

```
┌─────────┐     gRPC      ┌────────────┐     port-forward     ┌─────────────┐
│   CLI   │──────────────▶│   Daemon   │────────────────────▶│  Kubernetes │
└─────────┘  Unix Socket  └────────────┘    kubectl logic     └─────────────┘
                                │
                                ▼
                         Health Checks
                         Auto-Restart
                         Lifecycle Hooks
```

## Requirements

- Kubernetes cluster access (via kubeconfig)
- `kubectl` is NOT required - uses client-go directly

## License

MIT License - see [LICENSE](LICENSE) file.
