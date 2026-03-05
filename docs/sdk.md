# Client Library (`pkg/proxy`)

The `pkg/proxy` package lets your Go application transparently resolve Kubernetes service DNS names to `localhost` ports when running locally with kubeport.

## Installation

```bash
go get github.com/rbaliyan/kubeport/pkg/proxy
```

## Quick Start

```go
package main

import (
    "net/http"
    "log"

    "github.com/rbaliyan/kubeport/pkg/proxy"
)

func main() {
    p, err := proxy.New(proxy.WithEnabled(true))
    if err != nil {
        log.Fatal(err)
    }
    defer p.Close()

    client := &http.Client{
        Transport: &http.Transport{DialContext: p.DialContext},
    }

    // "my-api.default.svc.cluster.local:8080" → "localhost:8080"
    resp, err := client.Get("http://my-api.default.svc.cluster.local:8080/health")
    if err != nil {
        log.Fatal(err)
    }
    resp.Body.Close()
}
```

## How It Works

When you call `proxy.New()`, the library:

1. **Discovers** the running kubeport daemon (via config file or standard socket paths)
2. **Fetches** the current address mapping table over gRPC (e.g., `redis-0.redis.default.svc.cluster.local` → `127.0.0.1:16379`)
3. **Registers** a custom gRPC resolver (`kubeport:///` scheme)
4. Returns a `Proxy` that translates addresses in `DialContext` calls

All Kubernetes DNS variants are mapped automatically:
- `redis` → `127.0.0.1:16379`
- `redis.default` → `127.0.0.1:16379`
- `redis.default.svc` → `127.0.0.1:16379`
- `redis.default.svc.cluster.local` → `127.0.0.1:16379`

## Options

| Option | Description |
|--------|-------------|
| `WithEnabled(true)` | Return noop proxy on connection errors (graceful fallback) |
| `WithEnabled(false)` | Always return noop proxy without connecting |
| `WithSocketPath(path)` | Connect to a specific Unix socket |
| `WithTCP(host, apiKey)` | Connect to a remote daemon over TCP |
| `WithClusterDomain(domain)` | Override cluster domain (default: `cluster.local`) |
| `WithLogger(logger)` | Set a `*slog.Logger` for structured logging |

### The `WithEnabled` Tri-State

`WithEnabled` accepts a boolean but has three behaviors based on whether it's called:

| Call | On Success | On Error |
|------|-----------|----------|
| _not called_ (default) | Active proxy | Returns error |
| `WithEnabled(true)` | Active proxy | Returns noop proxy + logs warning |
| `WithEnabled(false)` | Returns noop proxy | _(never connects)_ |

Use `WithEnabled(true)` for code that must work both locally with kubeport and in-cluster without it.

## Context Support

`DialContext` fully respects the provided `context.Context` for timeouts and cancellation:

```go
ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
defer cancel()

conn, err := p.DialContext(ctx, "tcp", "redis.default.svc.cluster.local:6379")
if err != nil {
    // Handles both timeout and cancellation
    log.Printf("dial failed: %v", err)
}
```

The context flows through to the underlying `net.Dialer`, so cancelling the context immediately tears down the in-progress connection. This works correctly with `http.Client`, `grpc.Dial`, go-redis, and any library that passes a context to its dialer.

Internal gRPC calls to the kubeport daemon (initial `Mappings` fetch and `Refresh`) use a 5-second timeout independently of the dial context.

## Integration Examples

### go-redis

```go
import "github.com/redis/go-redis/v9"

p, _ := proxy.New(proxy.WithEnabled(true))
defer p.Close()

rdb := redis.NewClient(&redis.Options{
    Addr:    "redis.default.svc.cluster.local:6379",
    Dialer:  p.DialFunc(),
})
```

### Redis Sentinel

Redis Sentinel returns internal pod addresses (e.g., `redis-0.redis.default.svc.cluster.local:6379`) that aren't resolvable from localhost. The proxy translates these automatically:

```go
rdb := redis.NewFailoverClient(&redis.FailoverOptions{
    MasterName:    "mymaster",
    SentinelAddrs: []string{"redis.default.svc.cluster.local:26379"},
    Dialer:        p.DialFunc(),
})
```

### gRPC

For gRPC connections, use `GRPCTarget` to get a target string that routes through the kubeport resolver:

```go
import "google.golang.org/grpc"

p, _ := proxy.New(proxy.WithEnabled(true))
defer p.Close()

conn, err := grpc.NewClient(
    p.GRPCTarget("my-service.default.svc.cluster.local:9090"),
    grpc.WithTransportCredentials(insecure.NewCredentials()),
)
```

When kubeport is active, `GRPCTarget` returns `kubeport:///my-service.default.svc.cluster.local:9090`, which the registered resolver translates to the correct localhost address. When using a noop proxy, it returns the address unchanged.

### net/http

```go
client := &http.Client{
    Transport: &http.Transport{
        DialContext: p.DialContext,
    },
}

resp, err := client.Get("http://my-api.default.svc.cluster.local:8080/v1/users")
```

### database/sql (PostgreSQL)

```go
import "github.com/jackc/pgx/v5"

p, _ := proxy.New(proxy.WithEnabled(true))
defer p.Close()

config, _ := pgx.ParseConfig("postgres://user:pass@postgres.default.svc.cluster.local:5432/mydb")
config.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
    return p.DialContext(ctx, network, addr)
}
```

## Refreshing Mappings

If kubeport services change while your application is running, call `Refresh()` to re-fetch the address table:

```go
if err := p.Refresh(); err != nil {
    log.Printf("failed to refresh mappings: %v", err)
}
```

## Inspecting Mappings

Use `Addrs()` to see the current address translation table:

```go
for k8sAddr, localAddr := range p.Addrs() {
    fmt.Printf("%s → %s\n", k8sAddr, localAddr)
}
```

Or use the CLI:

```bash
kubeport mappings              # Human-readable grouped output
kubeport mappings --json       # JSON for scripting
kubeport mappings --yaml       # YAML for config generation
```
