# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
just build          # build bin/kubeport with embedded version (go-version ldflags)
just test           # go test ./...
just test-v         # verbose
just test-race      # with race detector
just lint           # golangci-lint run
just fmt            # go fmt ./...
just tidy           # go mod tidy
just proto          # buf generate (regenerate api/ from proto/)
just check          # fmt + lint + test

# Run a single test
go test -run TestName ./pkg/config/...
```

## Architecture

kubeport is a **daemon-per-config supervisor**. Each `kubeport.yaml` runs as a separate background process identified by a stable hash of its config path. The CLI communicates with daemons via gRPC.

### Communication model

```
CLI (internal/cli/) ──gRPC──► Daemon (internal/daemon/) ──► Manager (internal/proxy/)
                                Unix socket (default)
                                TCP + TLS + API key (remote mode)
```

The daemon listens on a Unix socket at `~/.config/kubeport/<instance-id>.sock`. TCP mode (for remote daemons) requires `listen: tcp://host:port` and an `api_key` in config. `pkg/grpcauth` provides the Bearer token interceptors.

### Daemon internals (`internal/proxy/`)

`Manager` owns a map of `portForward` structs, one per Kubernetes service/port pair. Each runs in its own goroutine:

1. Resolves the K8s service to running pods (label selectors via `client-go`)
2. Opens an SPDY tunnel using `k8s.io/client-go`'s port-forward dialer
3. Runs a health-check loop (TCP probe every N seconds)
4. Restarts on failure with exponential backoff (default 1s→30s, reset after 30s uptime)
5. Watches for pod termination to preemptively reconnect during rolling updates (`pod_watcher.go`)

`transport_cache.go` pools SPDY connections across multiple forwards to the same cluster.

`ports: all` in config expands into separate supervised `portForward` instances named `parent/portname`, tracked via `children` map.

**Connection mode dispatch** (`superviseSingle` in `manager.go`): the per-forward runner is selected in priority order — `isolated > lazy > mux`. `connection_mode: isolated` (`isolated.go`) is checked first; if set, `runIsolatedPortForward` is called regardless of `lazy`. Each client TCP connection gets its own SPDY tunnel; no shared stream cap. `ResolveConnectionMode` in `merge.go` is the single resolution point (per-service overrides supervisor default, falls back to `"mux"`). Isolated mode is incompatible with multi-port mode and is validated in `ValidateService`.

**Lazy mode** (`lazy.go`): binds the local port but defers opening the SPDY tunnel until the first client connection arrives. Shares `spdyForwardConn` with isolated mode.

**Network/chaos simulation** (`throttle.go`, `chaos.go`): injected at the TCP relay layer between the local port and the SPDY tunnel.

**External-conflict tracking**: `Manager.externalForwards` (guarded by `externalMu`) holds services from the local config that another running instance already owns. `SetExternalForwards` is called at startup and on every reload; it returns the names of services that just transitioned to `external` so the caller can `RemoveService` any local goroutine still running for them. The lock-ordering rule is **`mu` first, `externalMu` second** — never the reverse — and is asserted in the godoc on `Manager`. `Status()` merges entries from `externalForwards` into the result with `State == StateExternal`.

**Delegate teardown**: `Manager.ReleaseBySource(sourceConfig)` walks `forwards`, collects every entry whose `portForward.sourceConfig` equals the supplied path (rolling multi-port children up to their parents via `m.children`), and calls `RemoveService` on each. The daemon server exposes this as the `ReleaseBySource` RPC in `daemon.proto`; delegate instances call it at shutdown to clean up exactly what they contributed.

### Config system (`pkg/config/`)

Public package (also used by `pkg/proxy` SDK). Key behaviors:
- Supports YAML and TOML; format detected by file extension
- `ports` field is polymorphic: string `"all"` or list of port names — handled by custom `UnmarshalYAML` / `parsePortsFromRaw`
- TOML requires intermediate structs (`configTOML`, `serviceConfigTOML`) because `go-toml` lacks `any` unmarshal hooks
- `extends` field triggers config inheritance: `Load()` calls `loadWithInheritance()` which recursively resolves the chain (cycle detection via `visiting []string`), then `mergeConfigs()` (in `merge.go`) merges parent → child before env overrides are applied
- `Load()` applies env overrides after inheritance: `K8S_CONTEXT`, `K8S_NAMESPACE`, `KUBEPORT_API_KEY`
- `LoadForEdit()` bypasses inheritance — returns the raw file for in-place editing without resolving `extends`
- `chaos.enabled`, `socks.enabled`, `http_proxy.enabled` are `*bool`: nil = not set / inherit, `&true` = on, `&false` = explicitly off; use `boolVal()` / `IsEnabled()` to dereference safely
- Instance ID = SHA256 of the absolute config path (stable, human-readable prefix from parent dir name)
- `ResolveNetwork()` merges per-service and global network config field-by-field; `ResolveChaos()` uses per-service config wholesale when its `Enabled` is set
- Duration fields (supervisor, network, chaos) are stored as strings and parsed on demand via `ParsedSupervisor()` / `.Parse()`

Config discovery order: `./kubeport.yaml` → `./.kubeport.yaml` → `~/.config/kubeport/` → `~/.kubeport/`

### Instance registry (`internal/registry/`)

`~/.config/kubeport/instances.json` (flock-protected) tracks all running daemon instances. Used by `kubeport instances`, the `--offload` flag (route a service to an existing daemon), the `--delegate` flag (entries set `Delegate: true` and `PrimarySocket: <path>`), the auto external-conflict scan (only non-delegate primaries are considered owners), and stale-entry pruning. Delegate teardown calls the primary's `ReleaseBySource` RPC, keyed off the `source_config` runtime field carried on each `ServiceConfig`.

### Lifecycle hooks (`internal/hook/`)

`Dispatcher` fires events (`manager:starting`, `forward:connected`, `forward:failed`, etc.) to matching hooks. Hook types: `shell` (`sh -c`), `exec` (binary with template args), `webhook` (HTTP POST). Gate hooks (`fail_mode: closed`) block the triggering operation until the hook completes.

### Client SDK (`pkg/proxy/`)

Public package for application use. Provides transparent address translation: resolves Kubernetes DNS names (e.g., `redis.default.svc.cluster.local:6379`) to `localhost:<forwarded-port>`. Exposes `DialContext`, `DialFunc`, `GRPCTarget`, and a gRPC `Resolver` that integrates with gRPC's name resolution system.

### Protobuf

Proto source lives in `proto/kubeport/v1/daemon.proto`. Generated Go code is committed to `api/kubeport/v1/`. Run `just proto` (wraps `buf generate`) to regenerate. Do not edit files in `api/` manually.

### Version embedding

The binary embeds build metadata via `github.com/rbaliyan/go-version` ldflags injected during `just build`. The `just build` recipe reads the correct flags — always use it rather than `go build` directly when producing release artifacts.
