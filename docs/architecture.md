# Architecture

## Overview

kubeport runs as a background daemon that maintains persistent port-forward connections to your Kubernetes cluster. A lightweight CLI communicates with the daemon over a Unix socket using gRPC.

```
┌─────────────┐       gRPC        ┌──────────────────────────┐
│             │                   │         Daemon           │
│    CLI      │◄─────────────────►│                          │
│             │   Unix socket     │  ┌───────────────────┐   │      port-forward       ┌──────────────┐
└─────────────┘                   │  │  Proxy Manager    │───┼──────────────────────►  │  Kubernetes  │
                                  │  │                   │   │   client-go / SPDY       │  Cluster     │
                                  │  │  - Health checks  │   │                          └──────────────┘
                                  │  │  - Auto-restart   │   │
                                  │  │  - Backoff        │   │
                                  │  └───────────────────┘   │
                                  │                          │
                                  │  ┌───────────────────┐   │
                                  │  │  Hook Dispatcher  │───┼──── shell / exec / webhook
                                  │  └───────────────────┘   │
                                  └──────────────────────────┘
```

## Components

### CLI (`internal/cli/`)

Parses commands and flags, then either:
- Launches the daemon (for `start` / `fg`)
- Connects to a running daemon via gRPC (for `status`, `stop`, `add`, etc.)

### Proxy Manager (`internal/proxy/`)

The core engine. Manages one goroutine per port-forward with:

- **Service resolution** — translates Kubernetes Service names into running pods using label selectors. Handles named `targetPort` resolution from pod container specs.
- **Multi-port expansion** — when a service is configured with `ports: all` or a list of port names, the manager queries the Kubernetes API for the service's ports and spawns an independent supervised goroutine for each. Each expanded forward is named `parent/portname` (e.g., `my-api/http`) and has its own health checks, restart tracking, and status entry. A parent-child map tracks the relationship for removal and reload.
- **Port-forward lifecycle** — creates SPDY connections via client-go (the same library kubectl uses), waits for readiness, and extracts the actual local port.
- **Health checks** — periodic TCP probes to verify the forward is alive. After a configurable number of consecutive failures, the forward is restarted.
- **Auto-restart with backoff** — exponential backoff from 1s to 30s with 25% jitter. Backoff resets if a connection stays healthy for more than 30 seconds.
- **Thread-safe status reporting** — all forward states are tracked and queryable at any time.

### Daemon Server (`internal/daemon/`)

A gRPC server listening on a Unix socket (permissions `0600`) or TCP with API key auth. Errors are returned as proper gRPC status codes (`InvalidArgument`, `NotFound`, `AlreadyExists`, etc.). Exposes RPCs for:

- `Status` — current state of all forwards
- `Stop` — graceful shutdown
- `AddService` / `RemoveService` — dynamic management
- `Reload` — diff config on disk and sync changes
- `Apply` — merge in services from an overlay file
- `Mappings` — address translation table for the client SDK

### Client SDK (`pkg/proxy/`)

A lightweight Go library that lets applications transparently resolve Kubernetes service DNS names to localhost ports. Connects to the daemon over gRPC, fetches address mappings, and provides `DialContext`, `DialFunc`, and `GRPCTarget`/`GRPCDialOption` for integration with HTTP clients, go-redis, gRPC, and database drivers. See the [SDK guide](sdk.md) for usage.

### Hook Dispatcher (`internal/hook/`)

Dispatches lifecycle events to configured hooks. Supports three execution models:

- **Shell** — runs commands via `sh -c` with environment variables
- **Exec** — runs binaries directly with template-expanded arguments
- **Webhook** — POSTs JSON to HTTP endpoints

Hooks can operate as **gates** (using `fail_mode: closed`) to block operations like startup.

### Configuration (`pkg/config/`)

Loads and validates YAML/TOML config files. Manages file discovery, environment variable overrides, and path resolution for PID files, log files, and sockets. Supports two service modes: single-port (explicit `remote_port`/`local_port`) and multi-port (`ports: all` or a list of named ports), with custom YAML and TOML unmarshalers to handle the polymorphic `ports` field. This is a public package so that `pkg/proxy` (the client SDK) can share config parsing and discovery without importing Kubernetes dependencies.

### Auth (`pkg/grpcauth/`)

Provides gRPC unary interceptors for API key authentication (Bearer token). Used by both the daemon server (server interceptor) and clients connecting over TCP (client interceptor).

## How a Port-Forward Works

1. **Resolve target** — If a Service is specified, kubeport fetches the Service object, extracts its pod selector, lists matching Running pods, and resolves the service port to a container port (including named targetPort lookup). For multi-port services, this step also queries all service ports and expands each into a separate forward.
2. **Establish connection** — Creates an SPDY transport and dials the Kubernetes API server to set up a port-forward tunnel to the target pod.
3. **Wait for ready** — Blocks until the tunnel is established or the ready timeout expires. Extracts the actual local port (important when using dynamic port `0`).
4. **Health-check loop** — Periodically opens a TCP connection to `localhost:<port>` to verify the tunnel is alive.
5. **Handle failure** — If health checks fail or the tunnel drops, increments the restart counter, applies backoff delay (with jitter), and loops back to step 1.
6. **Backoff reset** — If a connection stays healthy for 30+ seconds, the backoff resets to the initial value.

## Design Decisions

- **No kubectl dependency** — Uses client-go directly for API access, making kubeport a single self-contained binary.
- **Unix socket for IPC** — Simpler than TCP, naturally scoped to the local machine, and permissions restrict access to the socket owner.
- **Daemon-per-config** — Each config file gets its own daemon instance, identified by its socket path. This allows multiple independent sets of forwards.
- **gRPC for control** — Typed RPC interface makes it easy to add new commands and maintain backwards compatibility.
