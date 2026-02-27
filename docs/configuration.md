# Configuration

kubeport uses a simple config file to define which Kubernetes services and pods to forward. Both YAML and TOML formats are supported.

## Config File Discovery

kubeport searches for its config file in this order:

1. Path specified via `--config` / `-c` flag
2. `kubeport.yaml`, `kubeport.yml`, or `kubeport.toml` in the current directory
3. `~/.config/kubeport/kubeport.{yaml,yml,toml}`
4. `~/.kubeport/kubeport.{yaml,yml,toml}`

The first match wins.

## Creating a Config File

Generate a starter config interactively:

```bash
kubeport config init
```

Or copy one of the examples in the repository:
- [example.yaml](../example.yaml)
- [example.toml](../example.toml)

## Config Reference

### Top-Level Fields

| Field | Type | Description |
|-------|------|-------------|
| `context` | string | Kubernetes context from your kubeconfig |
| `namespace` | string | Default namespace for all services |
| `log_file` | string | Custom log file path (default: `.kubeport.log` next to config) |
| `listen` | string | Daemon socket address (default: Unix socket next to config). Use `sock://` prefix for custom path |
| `services` | list | Services to port-forward (see below) |
| `supervisor` | object | Supervisor tuning (see below) |
| `hooks` | list | Lifecycle hooks (see [Hooks](hooks.md)) |

### Service Fields

Each entry in `services` defines one port-forward:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Display name for this forward |
| `service` | string | one of `service` or `pod` | Kubernetes Service name (auto-resolves to a running pod) |
| `pod` | string | one of `service` or `pod` | Specific pod name to forward to directly |
| `local_port` | int | yes | Local port to listen on. Use `0` for automatic assignment |
| `remote_port` | int | yes | Port on the pod to forward to |
| `namespace` | string | no | Override the top-level namespace for this service |

### Supervisor Fields

The `supervisor` section tunes restart and health-check behavior. All fields are optional with sensible defaults:

| Field | Default | Description |
|-------|---------|-------------|
| `max_restarts` | `0` (unlimited) | Stop retrying after N restarts |
| `health_check_interval` | `10s` | How often to probe connectivity |
| `health_check_threshold` | `3` | Consecutive failures before restart |
| `ready_timeout` | `15s` | Timeout waiting for a forward to become ready |
| `backoff_initial` | `1s` | Initial delay between restarts |
| `backoff_max` | `30s` | Maximum delay between restarts |

## YAML Example

```yaml
context: my-cluster-context
namespace: default

services:
  - name: My API
    service: my-api-service
    local_port: 8080
    remote_port: 80

  - name: Redis
    pod: redis-0
    local_port: 6379
    remote_port: 6379

  - name: Vault
    service: vault
    local_port: 8200
    remote_port: 8200
    namespace: vault

  - name: Debug Server
    service: debug-svc
    local_port: 0          # OS picks an available port
    remote_port: 9090

supervisor:
  max_restarts: 10
  health_check_interval: 10s
  health_check_threshold: 3
```

## TOML Example

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

[[services]]
name = "Vault"
service = "vault"
local_port = 8200
remote_port = 8200
namespace = "vault"
```

## Environment Variables

These override config file values:

| Variable | Description |
|----------|-------------|
| `K8S_CONTEXT` | Override Kubernetes context |
| `K8S_NAMESPACE` | Override default namespace |

## Managing Config via CLI

```bash
kubeport config show              # Display current config
kubeport config validate          # Check config for errors
kubeport config set context dev   # Set Kubernetes context
kubeport config set namespace app # Set default namespace
kubeport config add [options]     # Add a service
kubeport config remove <name>     # Remove a service
kubeport config path              # Print resolved config file path
```
