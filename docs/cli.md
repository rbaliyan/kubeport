# CLI Reference

## Commands

### `kubeport start`

Start the port-forward daemon in the background.

```bash
kubeport start                  # Start and return immediately
kubeport start --wait           # Start and wait until all forwards are ready
kubeport start --wait --timeout 30s
kubeport start --offload        # Send services to an already-running daemon instead of starting a new one
```

| Flag | Description |
|------|-------------|
| `--wait` | Block until all forwards are connected |
| `--timeout` | Maximum time to wait for readiness (default: 30s) |
| `--offload` | Add this config's services to an already-running daemon instead of launching a new process |

### `kubeport stop`

Stop the running daemon and all port-forwards.

```bash
kubeport stop
```

### `kubeport status`

Show the status of all port-forwards.

```bash
kubeport status                 # Human-readable table
kubeport status --json          # JSON output
kubeport status --sort          # Sort by name
```

### `kubeport logs`

Follow the daemon log file.

```bash
kubeport logs
```

### `kubeport restart`

Stop and start the daemon.

```bash
kubeport restart
```

### `kubeport fg`

Run in the foreground (blocking). Useful for debugging or running in containers.

```bash
kubeport fg
```

### `kubeport add`

Dynamically add a service to the running daemon.

```bash
# Single-port
kubeport add --name "My API" --service my-api --remote-port 80 --local-port 8080
kubeport add --name "Redis" --pod redis-0 --remote-port 6379 --local-port 6379
kubeport add --name "Vault" --service vault --remote-port 8200 --local-port 8200 -n vault

# Multi-port
kubeport add --name "Platform" --service platform-svc --ports all
kubeport add --name "Backend" --service backend-svc --ports http,grpc
kubeport add --name "Platform" --service platform-svc --ports all --exclude-ports metrics --local-port-offset 10000

# Persist the addition to the config file
kubeport add --name "My API" --service my-api --remote-port 80 --local-port 8080 --persist
```

| Flag | Required | Description |
|------|----------|-------------|
| `--name` | yes | Display name for the service |
| `--service` | one of `--service` or `--pod` | Kubernetes Service name |
| `--pod` | one of `--service` or `--pod` | Specific pod name |
| `--remote-port` | yes (single-port) | Remote port on the pod |
| `--local-port` | no | Local port (default: same as remote, `0` for auto) |
| `--namespace`, `-n` | no | Override the default namespace |
| `--ports` | yes (multi-port) | `all` or comma-separated port names |
| `--exclude-ports` | no | Port names to skip (with `--ports all`) |
| `--local-port-offset` | no | Offset added to remote ports for local ports |
| `--persist` | no | Save the service to the config file |

Multi-port flags (`--ports`, `--exclude-ports`, `--local-port-offset`) are mutually exclusive with `--remote-port`/`--local-port`.

### `kubeport remove`

Remove a service from the running daemon.

```bash
kubeport remove "My API"
```

### `kubeport reload`

Reload the config file and apply changes (add new services, remove deleted ones).

```bash
kubeport reload
```

### `kubeport apply`

Add services from an overlay config file (without affecting existing services).

```bash
kubeport apply extra-services.yaml
```

### `kubeport config`

Manage configuration. See subcommands:

```bash
kubeport config init              # Create a new config file
kubeport config show              # Display current config
kubeport config validate          # Check config for errors
kubeport config set <key> <value> # Set a config value
kubeport config add [options]     # Add a service
kubeport config remove <name>     # Remove a service
kubeport config path              # Print config file path
```

### `kubeport mappings`

Show the Kubernetes DNS to localhost address mapping table. Requires a running daemon.

```bash
kubeport mappings                   # Human-readable grouped by service
kubeport mappings --json            # JSON output
kubeport mappings --yaml            # YAML output
kubeport mappings --cluster-domain custom.local  # Custom cluster domain
```

| Flag | Description |
|------|-------------|
| `--json` | JSON output (global flag) |
| `--yaml` | YAML output |
| `--cluster-domain` | Kubernetes cluster domain (default: `cluster.local`) |

### `kubeport socks`

Start a SOCKS5 proxy that translates Kubernetes service DNS names to localhost ports. Requires a running daemon. See [Proxy Servers](proxy.md) for full details.

```bash
kubeport socks                                          # Listen on 127.0.0.1:1080
kubeport socks --listen 127.0.0.1:9050                  # Custom listen address
kubeport socks --username admin --password secret        # With authentication
kubeport socks --cluster-domain custom.local             # Custom cluster domain
```

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--listen` | `-l` | `127.0.0.1:1080` | Address to listen on |
| `--username` | `-u` | _(none)_ | Authentication username |
| `--password` | `-p` | _(none)_ | Authentication password |
| `--cluster-domain` | | `cluster.local` | Kubernetes cluster domain |

### `kubeport http-proxy`

Start an HTTP/HTTPS proxy that translates Kubernetes service DNS names to localhost ports. Supports both plain HTTP forwarding and HTTPS CONNECT tunneling. Requires a running daemon. See [Proxy Servers](proxy.md) for full details.

```bash
kubeport http-proxy                                     # Listen on 127.0.0.1:3128
kubeport http-proxy --listen 0.0.0.0:8888               # Custom listen address
kubeport http-proxy --username admin --password secret   # With basic auth
```

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--listen` | `-l` | `127.0.0.1:3128` | Address to listen on |
| `--username` | `-u` | _(none)_ | Authentication username |
| `--password` | `-p` | _(none)_ | Authentication password |
| `--cluster-domain` | | `cluster.local` | Kubernetes cluster domain |

### `kubeport watch`

Live-updating dashboard that refreshes the status view periodically. Press `q` to exit.

```bash
kubeport watch                  # Refresh every 2s (default)
kubeport watch --time 5s        # Custom refresh interval
kubeport watch --sort           # Sort services by name
```

| Flag | Default | Description |
|------|---------|-------------|
| `--time` | `2s` | Refresh interval (Go duration) |
| `--sort` | off | Sort services alphabetically |

### `kubeport instances`

List all running kubeport daemon instances registered in the central instance registry (`~/.config/kubeport/instances.json`). Useful for diagnosing port conflicts and finding socket or log file paths.

```bash
kubeport instances           # Human-readable table
kubeport instances --json    # JSON output
```

Each row shows the PID, uptime, version, gRPC endpoint (Unix socket or TCP address), API key hint, and config file path. A detail block below the table includes the full paths to the PID file and log file for each instance.

### `kubeport chaos`

Mutate chaos engineering settings on live tunnels without restarting or reloading the daemon. Changes apply immediately via an atomic pointer swap — active connections are not interrupted.

```bash
# Apply explicit params to one or more services
kubeport chaos set postgres --error-rate 0.05
kubeport chaos set postgres --latency 200ms --spike-prob 0.1

# Apply a named preset
kubeport chaos preset slow-network postgres redis
kubeport chaos preset unstable-cluster --all

# Enable / disable without changing params
kubeport chaos enable postgres
kubeport chaos disable --all

# Revert to config-defined settings
kubeport chaos reset postgres
kubeport chaos reset --all
```

#### `chaos set`

| Flag | Short | Description |
|------|-------|-------------|
| `--error-rate` | `-e` | Fraction of writes that fail (0.0–1.0) |
| `--latency` | `-l` | Latency spike duration (Go duration, e.g. `500ms`) |
| `--spike-prob` | `-p` | Probability of a latency spike per write (0.0–1.0); defaults to `1.0` when `--latency` is given without an explicit probability |
| `--all` | | Target all forwarded services |

#### `chaos preset`

```bash
kubeport chaos preset <name> [<service>...] [--all]
```

| Preset | Effect |
|--------|--------|
| `slow-network` | 200 ms latency spikes at 10% probability |
| `unstable-cluster` | 5% errors + 5% probability of 2 s latency spikes |
| `packet-loss` | 15% connection errors |

#### `chaos enable` / `chaos disable`

Enable or disable chaos injection for the given services using their current parameters. Does not change the underlying config or override values.

#### `chaos reset`

Revert to the chaos settings defined in the config file (discards any runtime override set via `chaos set` or `chaos preset`).

### `kubeport version`

Print the version.

```bash
kubeport version
```

## Global Flags

| Flag | Short | Description |
|------|-------|-------------|
| `--config` | `-c` | Path to config file |
| `--context` | | Kubernetes context (overrides config) |
| `--kube-context` | | Alias for `--context` |
| `--namespace` | `-n` | Default namespace (overrides config) |
| `--svc` | | Inline service spec (repeatable). Format: `name:type/target:remote:local[:namespace]` or `name:type/target:all[:+offset[:namespace]]` for multi-port |
| `--disable-svc` | | Disable a named service from config (repeatable) |
| `--no-config` | | Ignore config files, use only `--svc` flags |
| `--api-key` | | API key for TCP daemon authentication |
| `--host` | | Connect to a remote daemon over TCP (requires `--api-key`) |
| `--json` | | JSON output for commands that support it |
| `--sort` | | Sort output |
| `--wait` | | Wait for readiness (used with `start`) |
| `--timeout` | | Timeout for `--wait` |
| `--time` | | Refresh interval for `watch` (default: `2s`) |
| `--help` | `-h` | Show help |
| `--version` | `-v` | Show version |

## CLI-Only Mode

You can use kubeport without a config file by passing services inline:

```bash
kubeport start --no-config \
  --context my-cluster \
  --namespace default \
  --svc "api:svc/my-api:80:8080" \
  --svc "redis:pod/redis-0:6379:6379" \
  --svc "platform:svc/platform-svc:all" \
  --svc "backend:svc/backend-svc:http,grpc:+10000"
```
