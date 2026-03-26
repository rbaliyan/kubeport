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
| `listen` | string | Daemon socket address (default: Unix socket next to config). Use `sock://` prefix for custom path or `tcp://` for TCP |
| `api_key` | string | API key for TCP listener authentication (required when using TCP listen) |
| `host` | string | Hostname for the daemon |
| `network` | object | Global network simulation settings (see below) |
| `chaos` | object | Global chaos engineering settings (see below) |
| `services` | list | Services to port-forward (see below) |
| `supervisor` | object | Supervisor tuning (see below) |
| `hooks` | list | Lifecycle hooks (see [Hooks](hooks.md)) |
| `socks` | object | SOCKS5 proxy settings (see [Proxy Servers](proxy.md)) |
| `http_proxy` | object | HTTP proxy settings (see [Proxy Servers](proxy.md)) |

### Service Fields

Each entry in `services` defines one or more port-forwards. There are two modes:

**Single-port mode** (legacy) — specify `remote_port` and `local_port` explicitly:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Display name for this forward |
| `service` | string | one of `service` or `pod` | Kubernetes Service name (auto-resolves to a running pod) |
| `pod` | string | one of `service` or `pod` | Specific pod name to forward to directly |
| `local_port` | int | yes | Local port to listen on. Use `0` for automatic assignment |
| `remote_port` | int | yes | Port on the pod to forward to |
| `namespace` | string | no | Override the top-level namespace for this service |

**Multi-port mode** — automatically discover and forward multiple ports from a service:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Display name (expanded forwards are named `name/portname`) |
| `service` | string | one of `service` or `pod` | Kubernetes Service name |
| `pod` | string | one of `service` or `pod` | Specific pod name |
| `ports` | string or list | yes | `"all"` to forward every port, or a list of port names/selectors |
| `exclude_ports` | list | no | Port names to skip (only with `ports: all`) |
| `local_port_offset` | int | no | Add this offset to each remote port to compute the local port |
| `namespace` | string | no | Override the top-level namespace for this service |
| `network` | object | no | Per-service network simulation (overrides global `network`) |
| `chaos` | object | no | Per-service chaos injection (overrides global `chaos`) |

Multi-port mode is mutually exclusive with `remote_port`/`local_port`. When no local port is specified, each forwarded port defaults to the same number as the remote port.

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
| `max_connection_age` | `0` (disabled) | Maximum lifetime of a port-forward before proactive reconnect |

### Network Simulation Fields

The `network` section configures latency injection and bandwidth throttling for testing. Can be set globally or per-service.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `latency` | string | _(none)_ | Added latency per write (Go duration, e.g. `"50ms"`) |
| `jitter` | string | _(none)_ | Random jitter added to latency (must not exceed `latency`) |
| `bandwidth` | string | _(none)_ | Bandwidth cap (e.g. `"5mbps"`, `"500kbps"`, `"1gbps"`, `"1mbytes"`) |

Per-service `network` settings override the global settings. If a service specifies only some fields, the remaining fields are inherited from the global config (field-by-field merge).

### Chaos Engineering Fields

The `chaos` section configures fault injection for testing resilience. Can be set globally or per-service.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Master switch for chaos injection |
| `error_rate` | float | `0.0` | Fraction of writes that fail with a connection error (0.0-1.0) |
| `latency_spike.probability` | float | `0.0` | Probability of a latency spike on each write (0.0-1.0) |
| `latency_spike.duration` | string | _(none)_ | Duration of latency spikes (Go duration, e.g. `"5s"`) |

Per-service `chaos` settings fully override global when `enabled: true` (unlike `network`, which does field-by-field merge). The global `enabled` flag acts as a master switch.

### Proxy Server Fields

The `socks` and `http_proxy` sections configure the built-in proxy servers. See [Proxy Servers](proxy.md) for usage details.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `listen` | string | `127.0.0.1:1080` (SOCKS) / `127.0.0.1:3128` (HTTP) | Listen address |
| `username` | string | _(none)_ | Authentication username |
| `password` | string | _(none)_ | Authentication password |
| `fuzzy_match` | bool | `true` | Enable headless service FQDN resolution |

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

  # Multi-port: forward all ports from a service
  - name: Platform
    service: platform-svc
    ports: all

  # Multi-port: forward specific named ports
  - name: My Backend
    service: backend-svc
    ports:
      - http
      - grpc

  # Multi-port: named ports with local port overrides
  - name: My Backend
    service: backend-svc
    ports:
      - name: http
        local_port: 8080
      - name: grpc

  # Multi-port: all ports except metrics, with offset
  - name: Platform
    service: platform-svc
    ports: all
    exclude_ports: [metrics]
    local_port_offset: 10000

supervisor:
  max_restarts: 10
  health_check_interval: 10s
  health_check_threshold: 3
  max_connection_age: 30m

# Global network simulation (optional, for testing)
network:
  latency: 50ms
  jitter: 10ms
  bandwidth: 5mbps

# Global chaos engineering (optional, for resilience testing)
chaos:
  enabled: true
  error_rate: 0.02           # 2% of writes fail
  latency_spike:
    probability: 0.01        # 1% chance of a spike
    duration: 5s             # 5 seconds of added lag

socks:
  listen: 127.0.0.1:1080
  # username: admin
  # password: secret

http_proxy:
  listen: 127.0.0.1:3128
  # username: admin
  # password: secret
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

# Multi-port: forward all ports from a service
[[services]]
name = "Platform"
service = "platform-svc"
ports = "all"

# Multi-port: specific named ports
[[services]]
name = "Backend"
service = "backend-svc"
ports = ["http", "grpc"]

[socks]
listen = "127.0.0.1:1080"
# username = "admin"
# password = "secret"

[http_proxy]
listen = "127.0.0.1:3128"
# username = "admin"
# password = "secret"
```

## Environment Variables

These override config file values:

| Variable | Description |
|----------|-------------|
| `K8S_CONTEXT` | Override Kubernetes context |
| `K8S_NAMESPACE` | Override default namespace |
| `KUBEPORT_API_KEY` | Override API key for TCP listener authentication |

## Managing Config via CLI

```bash
kubeport config show              # Display current config
kubeport config validate          # Check config for errors
kubeport config set context dev   # Set Kubernetes context
kubeport config set namespace app # Set default namespace
kubeport config add [options]     # Add a service (single-port)
kubeport config remove <name>     # Remove a service
kubeport config path              # Print resolved config file path
```

### Adding multi-port services via CLI

```bash
# Forward all ports
kubeport config add --name "Platform" --service platform-svc --ports all

# Forward specific named ports
kubeport config add --name "Backend" --service backend-svc --ports http,grpc

# All ports except metrics, with local port offset
kubeport config add --name "Platform" --service platform-svc \
  --ports all --exclude-ports metrics --local-port-offset 10000
```
