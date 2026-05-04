# Troubleshooting

Common issues and their solutions when using kubeport.

## Connection Issues

### Port-forward keeps restarting

**Symptoms:** Status shows high restart count, connection cycling between `connected` and `reconnecting`.

**Causes and fixes:**

1. **Pod is crash-looping** — Check pod status with `kubectl get pods`. If the pod is restarting, fix the underlying issue.

2. **Health check too aggressive** — If the target service is slow to respond to TCP probes, increase the threshold:
   ```yaml
   supervisor:
     health_check_threshold: 5    # default is 3
     health_check_interval: 15s   # default is 10s
   ```

3. **Network instability (VPN, proxy)** — If you're behind a VPN that periodically reconnects, kubeport will auto-recover. Consider adding a gate hook to block startup until VPN is ready:
   ```yaml
   hooks:
     - name: vpn-check
       type: shell
       events: [manager:starting]
       fail_mode: closed
       shell:
         manager:starting: ping -c1 -W2 your-cluster-api-host
   ```

### Connections stall or fail above ~100 simultaneous clients

**Symptoms:** More than ~100 concurrent connections to the same forward start stalling or failing. The daemon log shows `create stream` errors or `stream limit reached`. The forward itself stays up.

**Cause:** The default `mux` mode multiplexes all client connections over a single SPDY tunnel. The Kubernetes API server enforces a hard cap of 256 streams per SPDY connection, which translates to approximately 128 simultaneous clients (each client uses two streams: one data, one error). Workloads like Redis Sentinel — which hold pub/sub connections, polling connections, and connection pools to every sentinel at once — can easily exceed this cap.

**Fix:** Set `connection_mode: isolated` on the affected service. This gives each client its own SPDY tunnel, removing the cap entirely at the cost of one extra TLS handshake per client:

```yaml
services:
  - name: Redis Sentinel
    service: redis-sentinel
    local_port: 26379
    remote_port: 26379
    connection_mode: isolated
```

To apply this globally to all services, set it in the supervisor section:

```yaml
supervisor:
  connection_mode: isolated
```

See [Connection Modes](configuration.md#connection-modes) for a full comparison.

### "unable to create SPDY connection" or transport errors

The Kubernetes API server must be reachable and your kubeconfig credentials must be valid.

```bash
# Verify cluster access
kubectl cluster-info
kubectl auth can-i create pods/portforward
```

If using a cloud provider, ensure your credentials haven't expired:
```bash
# AWS EKS
aws eks update-kubeconfig --name my-cluster

# GCP GKE
gcloud container clusters get-credentials my-cluster

# Azure AKS
az aks get-credentials --resource-group mygroup --name my-cluster
```

### Local port already in use

**Symptom:** `bind: address already in use`

Another process is using the local port. Find it:

```bash
# macOS / Linux
lsof -i :8080

# Kill the process if needed
kill <PID>
```

Or change the local port in your config, or use `local_port: 0` for automatic assignment.

### Service shows as `external` in `kubeport status` / `watch`

**Symptom:** A service from your config file appears with the cyan `⤵` indicator and the label `external [managed by PID X]` instead of the usual green `●` running.

**Cause:** Another kubeport daemon (PID X) is already managing a service with either the same name or the same static `local_port`. To prevent two processes from binding the same port — or from racing on the same logical service — kubeport's startup and reload logic walks the instance registry and marks the conflict as `external` rather than starting a duplicate forward locally. Delegate instances are excluded from this scan; only primary daemons can "own" a service.

**What to do:**

1. **Inspect the owner.** `kubeport instances` shows the running daemons and their config paths — the `PID` column matches the `[managed by PID X]` annotation.
2. **Use the existing forward.** The service is reachable on whatever local port the owning daemon assigned. Run `kubeport status --host <socket>` against the owning daemon if you need the exact port.
3. **Reclaim it.** Stop the owning instance (`kubeport stop --config <its-config>`) and your next reload (`kubeport reload`, SIGHUP, or any config file change) will pick the service back up locally.
4. **Hand it off cleanly.** If you want the service centralised on the owning daemon for as long as your config is active, restart your daemon with `--delegate` instead. Your delegate will register the services on the primary and `ReleaseBySource` them automatically on shutdown. See [Multiple Instances and Delegate Mode](advanced-usage.md#multiple-instances-and-delegate-mode).

### Proxy cannot start — listen address already in use

**Symptom:** `kubeport start` (or `kubeport fg`) prints:

```
Error: SOCKS proxy cannot start — 127.0.0.1:1080 is already in use.
Only one kubeport instance may run a proxy on the same address.
```

(or the equivalent for `127.0.0.1:3128` and the HTTP proxy.)

**Cause:** Before auto-starting the proxy, kubeport dials its configured listen address. Something — usually another kubeport daemon, but it could be any process — already has the address bound. Unlike normal port-forwards, the proxy listeners cannot be shared between instances.

**What to do:**

- `lsof -i :1080` (or `:3128`) — confirm the holder.
- If it is another kubeport daemon, stop it or set a different `socks.listen` / `http_proxy.listen` in this config (e.g. `127.0.0.1:1081`).
- If the proxy is non-essential for this instance, set `socks.enabled: false` (or `http_proxy.enabled: false`) in the config and use `kubeport socks` / `kubeport http-proxy` to start it manually on demand.

## RBAC Permission Errors

### "forbidden: User cannot create resource pods/portforward"

Your Kubernetes user or service account lacks the required RBAC permissions. You need `create` on `pods/portforward` and `get` on `pods` and `services`:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: port-forward
rules:
  - apiGroups: [""]
    resources: ["pods", "pods/portforward"]
    verbs: ["get", "list", "create"]
  - apiGroups: [""]
    resources: ["services"]
    verbs: ["get", "list"]
```

Bind this role to your user or service account with a ClusterRoleBinding or namespace-scoped RoleBinding.

## Configuration Issues

### Config file not found

See [Config File Discovery](configuration.md#config-file-discovery) for the full search order.

Check which file kubeport resolves:
```bash
kubeport config path
```

### Config validation errors

Validate your config before starting:
```bash
kubeport config validate
```

Common mistakes:
- Using both `remote_port` and `ports` on the same service (mutually exclusive)
- Missing `name` field on a service
- Specifying neither `service` nor `pod`

## Daemon Issues

### "daemon is not running"

The daemon process may have exited or the socket file may be stale.

```bash
# Check if the process is running
ps aux | grep kubeport

# If the socket exists but no process is running, start fresh
kubeport start
```

### Cannot connect to daemon socket

The Unix socket is created with `0600` permissions (owner only). If you're running `kubeport start` as one user and `kubeport status` as another, you'll get a permission error.

### Logs location

Log files are stored in `~/.config/kubeport/logs/<instance-id>.log` (not next to the config file). Each daemon instance has its own log file named after its `<instance-id>`. Override the log path with:
```yaml
log_file: /tmp/kubeport.log
```

To find the log file for a running instance, use:
```bash
kubeport instances   # shows PID, uptime, endpoint, and log file path for each instance
kubeport logs        # follow logs for the current config's daemon
```

## Multi-Port Issues

### "no ports found for service"

The service exists but has no ports defined in its spec. Verify:
```bash
kubectl get svc my-service -o jsonpath='{.spec.ports[*].name}'
```

### Excluded port names not matching

Port exclusion (`exclude_ports`) matches against the port **name** in the Service spec, not the port number. If a port has no name, it cannot be excluded by name.

## Getting Help

If your issue isn't covered here:

1. Check `kubeport logs` for detailed error messages
2. Run in foreground mode for real-time output: `kubeport fg`
3. Open an issue on [GitHub](https://github.com/rbaliyan/kubeport/issues) with:
   - kubeport version (`kubeport version`)
   - Kubernetes version (`kubectl version`)
   - Relevant config (redact sensitive values)
   - Log output
