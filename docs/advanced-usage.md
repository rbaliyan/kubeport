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

Each project gets its own daemon instance (identified by the socket path next to the config file), so they run independently.

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
listen: tcp://0.0.0.0:9400?key=my-secret-api-key
```

Connect from another machine:
```bash
kubeport status --address tcp://host:9400?key=my-secret-api-key
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
