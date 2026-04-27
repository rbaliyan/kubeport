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
