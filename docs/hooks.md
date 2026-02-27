# Lifecycle Hooks

Hooks let you run custom actions when port-forward events happen — send a Slack notification when a connection drops, check VPN status before starting, log events to syslog, and more.

## Quick Example

```yaml
hooks:
  - name: notify
    type: shell
    shell:
      forward_connected: notify-send "kubeport" "${KUBEPORT_SERVICE} ready on port ${KUBEPORT_LOCAL_PORT}"
      forward_disconnected: echo "${KUBEPORT_SERVICE} disconnected"
```

## Hook Types

### Shell

Runs commands via `sh -c`. Each event maps to its own command string:

```yaml
- name: my-hook
  type: shell
  shell:
    forward_connected: echo "connected"
    forward_failed: echo "failed" >> /tmp/kubeport-failures.log
```

### Exec

Executes a binary directly (no shell). Arguments support `${VAR}` template expansion:

```yaml
- name: log-events
  type: exec
  exec:
    command: ["logger", "-t", "kubeport", "${EVENT}: ${SERVICE} port ${PORT}"]
```

### Webhook

Sends an HTTP POST with a JSON body to a URL:

```yaml
- name: slack-alert
  type: webhook
  events: [forward_failed]
  webhook:
    url: https://hooks.slack.com/services/T.../B.../xxx
    headers:
      Content-Type: application/json
    body_template: '{"text": "Port forward failed: ${SERVICE} - ${ERROR}"}'
```

## Events

| Event | When it fires |
|-------|---------------|
| `manager_starting` | Before any forwards begin. This is a **gate event** — a `closed` fail mode hook can block startup |
| `manager_stopped` | All forwards stopped, cleanup complete |
| `forward_connected` | A port-forward is ready and healthy |
| `forward_disconnected` | A port-forward dropped (will retry automatically) |
| `forward_failed` | A port-forward has permanently failed (max restarts exceeded) |
| `forward_stopped` | A port-forward was intentionally stopped |
| `health_check_failed` | A single health-check probe failed |
| `service_added` | A service was dynamically added |
| `service_removed` | A service was dynamically removed |

## Hook Options

| Option | Default | Description |
|--------|---------|-------------|
| `events` | all events | List of events this hook listens for |
| `timeout` | `10s` | Maximum time the hook can run |
| `fail_mode` | `open` | `open` = log and continue; `closed` = abort the operation if the hook fails |
| `filter_services` | all services | Only trigger for these named services |

## Environment Variables

Shell and exec hooks receive context about the event via environment variables:

| Variable | Description |
|----------|-------------|
| `KUBEPORT_EVENT` | Event name (e.g., `forward_connected`) |
| `KUBEPORT_SERVICE` | Service name from config |
| `KUBEPORT_LOCAL_PORT` | Actual local port number |
| `KUBEPORT_REMOTE_PORT` | Remote port number |
| `KUBEPORT_POD` | Resolved pod name |
| `KUBEPORT_RESTARTS` | Restart count for this forward |
| `KUBEPORT_ERROR` | Error message (when applicable) |

## Template Variables

Exec `command` arguments and webhook `body_template` support `${VAR}` expansion:

`${EVENT}`, `${SERVICE}`, `${PORT}`, `${REMOTE_PORT}`, `${POD}`, `${RESTARTS}`, `${ERROR}`, `${TIME}`

## Use Cases

### Gate startup on VPN

Block kubeport from starting unless a VPN is active:

```yaml
- name: vpn-check
  type: shell
  events: [manager_starting]
  fail_mode: closed
  timeout: 30s
  shell:
    manager_starting: ./scripts/ensure-vpn.sh
```

### Desktop notifications

```yaml
- name: notify
  type: shell
  shell:
    forward_connected: notify-send "kubeport" "${KUBEPORT_SERVICE} ready"
    forward_failed: notify-send -u critical "kubeport" "${KUBEPORT_SERVICE} failed: ${KUBEPORT_ERROR}"
```

### Slack alerts on failures

```yaml
- name: slack
  type: webhook
  events: [forward_failed]
  webhook:
    url: https://hooks.slack.com/services/...
    headers:
      Content-Type: application/json
    body_template: '{"text": ":warning: kubeport forward failed: ${SERVICE} - ${ERROR}"}'
```

### Log to syslog

```yaml
- name: syslog
  type: exec
  events: [forward_connected, forward_disconnected, forward_failed]
  exec:
    command: ["logger", "-t", "kubeport", "${EVENT}: ${SERVICE} on port ${PORT}"]
```

## Security

Hook commands come from your config file. Treat config files like scripts:

- Only use configs you trust
- Ensure config files are not world-writable
- Avoid interpolating untrusted input into hook commands
- Environment variables passed to hooks are sanitized (newlines and null bytes stripped)
