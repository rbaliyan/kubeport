# Advanced Usage

## Multiple Clusters

Use `KUBECONFIG` or the `context` field to target different clusters.

### Per-project configs

Keep a `kubeport.yaml` in each project directory with the appropriate context:

```yaml
# project-a/kubeport.yaml
context: dev-cluster
namespace: project-a
services:
  - name: API
    service: api-svc
    local_port: 8080
    remote_port: 80
```

```yaml
# project-b/kubeport.yaml
context: staging-cluster
namespace: project-b
services:
  - name: API
    service: api-svc
    local_port: 9080
    remote_port: 80
```

Each project gets its own daemon instance, identified by a stable `InstanceID` derived from the absolute path of its config file (parent directory name + SHA-256 hash prefix). Runtime files — socket, PID file, and log — are stored in the central directory (`~/.config/kubeport/`) under that `InstanceID`, so the instances run independently without file conflicts. Use `kubeport instances` to see all running daemons across all configs.

### Sharing a Global Config with `extends`

Keep `api_key`, `listen`, `context`, and supervisor tuning in one place and inherit them in every project config:

```yaml
# ~/.config/kubeport/global.yaml
api_key: sk-secret
listen: tcp://0.0.0.0:50500
context: prod-cluster
supervisor:
  health_check_interval: 10s
  max_restarts: 10
```

```yaml
# ~/projects/myapp/kubeport.yaml
extends: ~/.config/kubeport/global.yaml
namespace: myapp

services:
  - name: postgres
    service: postgres
    local_port: 5432
    remote_port: 5432
```

The child config overrides only the fields it sets; everything else is inherited from the parent. Chains are supported (A extends B extends C). See [Config Inheritance](configuration.md#config-inheritance) for full merge semantics.

### Multiple kubeconfig files

```bash
# Merge kubeconfig files
export KUBECONFIG=~/.kube/config:~/.kube/staging-config

# Then reference contexts from either file
kubeport config set context staging-cluster
kubeport start
```

### Switching clusters

```bash
# Update the context in your config
kubeport config set context new-cluster

# Reload the running daemon (restarts all forwards with the new context)
kubeport reload
```

## CI/CD Integration

kubeport is useful in CI pipelines that run integration tests against a Kubernetes cluster.

### GitHub Actions

```yaml
name: Integration Tests
on: [push]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Configure kubectl
        uses: azure/setup-kubectl@v4

      - name: Set kubeconfig
        run: echo "${{ secrets.KUBECONFIG }}" > ~/.kube/config

      - name: Install kubeport
        run: go install github.com/rbaliyan/kubeport@latest

      - name: Start port-forwards
        run: |
          kubeport fg &
          sleep 5  # wait for tunnels to establish

      - name: Run integration tests
        run: go test -tags=integration ./...

      - name: Stop kubeport
        if: always()
        run: kubeport stop
```

### Using a gate hook to wait for readiness

Instead of `sleep`, use a hook to signal readiness:

```yaml
# kubeport.yaml
hooks:
  - name: ready-signal
    type: shell
    events: [forward:connected]
    shell:
      forward:connected: touch /tmp/kubeport-ready
```

Then in CI:
```bash
kubeport fg &
while [ ! -f /tmp/kubeport-ready ]; do sleep 1; done
echo "Tunnels are ready"
go test -tags=integration ./...
```

### Makefile integration

```makefile
.PHONY: dev test-integration

# Start kubeport and run the app
dev:
	kubeport start
	go run ./cmd/server

# Run integration tests with port-forwards
test-integration:
	kubeport fg &
	sleep 3
	go test -tags=integration ./... ; kubeport stop
```

## Foreground Mode for Containers

When running in Docker or Kubernetes (e.g., a sidecar), use foreground mode since there's no background daemon management:

```bash
kubeport fg --config /etc/kubeport/kubeport.yaml
```

This runs in the foreground, logs to stdout, and exits on SIGTERM/SIGINT.

## Remote Daemon Control

kubeport supports TCP listeners with API key authentication for controlling the daemon remotely (e.g., from a different machine or container):

```yaml
# Separate fields (preferred)
listen: tcp://0.0.0.0:9400
api_key: my-secret-api-key

# Or embedded in the URL
# listen: tcp://0.0.0.0:9400?key=my-secret-api-key
```

Connect from another machine:
```bash
kubeport status --host host:9400 --api-key my-secret-api-key
```

## Dynamic Overlays

Apply additional services from overlay files without modifying the main config:

```bash
# Start with base config
kubeport start

# Apply an overlay for debugging
kubeport apply debug-services.yaml

# Remove overlay services later
kubeport remove "Debug Service"
```

This is useful for team workflows where the base config is checked into the repo and individuals add their own services on top.

## Multiple Instances and Delegate Mode

When more than one kubeport daemon is running on the same machine, kubeport exposes three distinct ways to combine them. Pick the one that matches your workflow:

| Approach | What it does | Process model | Lifecycle |
|----------|--------------|---------------|-----------|
| **Default `kubeport start` (auto external)** | Each project has its own daemon. If a service in your config is already owned by another running daemon (same name *or* static `local_port`), it is marked `external` in your status view and not started locally. The next reload reclaims the service when the other daemon exits. | Two independent primaries. | Each daemon owns its own services and lifetime. |
| **`kubeport start --offload`** | Sends this config's services to an already-running daemon and exits. Nothing is owned by the calling process. | One process (the existing primary). | Services persist on the primary until removed manually. |
| **`kubeport start --delegate`** | Starts a lease-holder daemon that hands its services to an existing primary, stays alive in the background, and calls `ReleaseBySource` on the primary at shutdown to bulk-remove only the services it contributed. | Two processes: primary + delegate. | Delegate-contributed services are released automatically when the delegate stops. |

### Auto external-conflict detection (default)

`kubeport start` with no flag is the safe default. At startup and on every reload (SIGHUP or config-file change) the daemon walks the central registry and decides, per service, whether another running primary already owns it. Ownership is matched by service name OR static `local_port`; delegate instances are skipped (they do not "own" services). External services show up in both `kubeport status` and `kubeport watch`:

```
  ⤵ Postgres: :5432 [external] [managed by PID 12345]
```

When PID 12345 stops, the next reload picks the service back up locally.

The same single-instance rule applies to the SOCKS and HTTP proxies: only one daemon may bind a given proxy listen address. If the address is already in use, kubeport refuses to auto-start that proxy and prints an explicit error rather than failing silently.

### Delegate mode

Use `--delegate` when you want the convenience of one shared primary daemon (single status view, single set of port-forwards, single proxy) but still want each project to manage the lifetime of its own services:

```bash
# Primary daemon already running
kubeport start --config ~/myproject/kubeport.yaml

# In another project, register as a delegate
kubeport start --config ~/edge/kubeport.yaml --delegate

# kubeport instances now lists both — the delegate row carries Primary: <socket>
kubeport instances

# Stop only the edge services: the delegate calls ReleaseBySource on the primary
kubeport stop --config ~/edge/kubeport.yaml
```

If no primary daemon is running when `--delegate` is invoked, kubeport falls back to a regular `start` and the new instance is registered as `primary`. Internally, the delegate runs a `noopSupervisor` (no local port-forwards) and re-execs itself with `--primary-socket <path>` — that flag is set automatically and is not intended for direct use.

`source_config` (the absolute path of the delegate's config) is set on every service the delegate adds. The primary keys its `ReleaseBySource` lookup off this field so a single RPC removes exactly the delegate's contributions and nothing else.

## High-Concurrency Forwards

By default, kubeport uses `mux` mode: one shared SPDY connection per port-forward, with all client TCP connections multiplexed over it. Because the Kubernetes API server caps SPDY at 256 streams and each client uses two streams, this limits you to roughly 128 simultaneous clients per forward.

For workloads that exceed this limit — Redis Sentinel (pub/sub + polling + pooled connections to every sentinel), database connection pools, or heavily parallelised test suites — switch to `isolated` mode:

```yaml
services:
  - name: Redis Sentinel
    service: redis-sentinel
    local_port: 26379
    remote_port: 26379
    connection_mode: isolated
```

In `isolated` mode each client TCP connection gets its own dedicated SPDY tunnel. There is no shared stream cap; concurrency is bounded only by available file descriptors and API server capacity. The only extra cost is one additional TLS handshake per client connection, which is typically negligible compared to the connection setup overhead.

To apply `isolated` mode globally as the default for every service in a config:

```yaml
supervisor:
  connection_mode: isolated
```

A per-service `connection_mode` always overrides the supervisor-level default.

`isolated` mode is not compatible with `ports: all` / multi-port forwards. See [Connection Modes](configuration.md#connection-modes) for the full comparison.

## Live Chaos Mutation

Chaos settings can be changed on running tunnels without restarting the daemon or reloading config. Changes take effect immediately via atomic pointer swap — active connections continue uninterrupted.

```bash
# Inject 5% errors on the postgres tunnel
kubeport chaos set postgres --error-rate 0.05

# Add 200ms latency spikes at 10% probability
kubeport chaos set postgres --latency 200ms --spike-prob 0.1

# Apply a preset to multiple services at once
kubeport chaos preset slow-network postgres redis

# Disable chaos injection without changing the underlying config
kubeport chaos disable --all

# Revert to config-defined settings
kubeport chaos reset postgres
```

### Built-in Presets

| Preset | Error rate | Latency spike | Probability |
|--------|-----------|---------------|-------------|
| `slow-network` | — | 200 ms | 10% |
| `unstable-cluster` | 5% | 2 s | 5% |
| `packet-loss` | 15% | — | — |

Presets are applied with `kubeport chaos preset <name> [<service>...] [--all]`. Runtime overrides do not persist across daemon restarts — the daemon always starts from the config-defined chaos settings. Use `kubeport chaos reset` to restore config-defined settings while the daemon is running.

## JSON Output for Scripting

```bash
# Machine-readable status
kubeport status --json

# Example: extract local port for a service
kubeport status --json | jq -r '.services[] | select(.name == "API") | .local_port'

# Use in scripts
API_PORT=$(kubeport status --json | jq -r '.services[] | select(.name == "API") | .local_port')
curl http://localhost:$API_PORT/health
```
