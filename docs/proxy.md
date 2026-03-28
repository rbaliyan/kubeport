# Proxy Servers

kubeport includes built-in SOCKS5 and HTTP proxy servers that translate Kubernetes service DNS names to localhost ports. Instead of configuring each tool with explicit `localhost:PORT` addresses, you point it at the proxy and use real Kubernetes service names.

## When to Use Proxies

| Approach | Best for |
|----------|----------|
| Direct port-forwards | Tools where you control the connection string (e.g., `psql -h localhost -p 5432`) |
| SOCKS5 proxy | Tools that support SOCKS proxies natively (browsers, curl, database GUIs) |
| HTTP proxy | HTTP/HTTPS traffic where `http_proxy` / `https_proxy` env vars are standard |
| [Go SDK](sdk.md) | Your own Go code — transparent address translation in `DialContext` and gRPC |

Proxies are especially useful when:

- A service returns internal DNS names that your local machine cannot resolve (e.g., Redis Sentinel returning `redis-0.redis.default.svc.cluster.local:6379`)
- You want to access services using their real Kubernetes names without remembering port mappings
- You need to route browser or GUI tool traffic through kubeport

## Prerequisites

The kubeport daemon must be running before starting a proxy:

```bash
kubeport start          # or: kubeport fg (foreground mode)
kubeport status         # verify forwards are active
```

## SOCKS5 Proxy

### Quick Start

```bash
kubeport socks
```

This starts a SOCKS5 proxy on `127.0.0.1:1080` (the standard SOCKS port).

### Usage

```bash
# curl
curl --proxy socks5h://127.0.0.1:1080 http://my-service:8080/api/health

# wget
wget -e use_proxy=yes -e all_proxy=socks5h://127.0.0.1:1080 http://my-service:8080/

# git (over SSH or HTTPS)
git -c http.proxy=socks5h://127.0.0.1:1080 clone https://internal-git:443/repo.git
```

> **Note:** Use `socks5h://` (with the `h`) so that DNS resolution happens on the proxy side. Plain `socks5://` resolves DNS locally, which defeats the purpose of address translation.

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--listen` | `-l` | `127.0.0.1:1080` | Address to listen on |
| `--username` | `-u` | _(none)_ | Enable authentication with this username |
| `--password` | `-p` | _(none)_ | Password for authentication |
| `--cluster-domain` | | `cluster.local` | Kubernetes cluster domain for FQDN matching |

### Authentication

Enable username/password authentication (RFC 1929) to restrict access:

```bash
kubeport socks --username admin --password secret
```

Clients must then include credentials:

```bash
curl --proxy socks5h://admin:secret@127.0.0.1:1080 http://my-service:8080/
```

Credentials are verified using constant-time comparison to prevent timing attacks.

### Protocol Details

- SOCKS5 (RFC 1928)
- Supports CONNECT command only (no BIND or UDP ASSOCIATE)
- Accepted address types: IPv4, IPv6, domain names
- 30-second handshake timeout to prevent slow-client resource exhaustion

## HTTP Proxy

### Quick Start

```bash
kubeport http-proxy
```

This starts an HTTP/HTTPS proxy on `127.0.0.1:3128` (the standard HTTP proxy port).

### Usage

```bash
# curl (explicit)
curl --proxy http://127.0.0.1:3128 http://my-service:8080/api/health

# Environment variables (applies to most tools)
export http_proxy=http://127.0.0.1:3128
export https_proxy=http://127.0.0.1:3128
curl http://my-service:8080/api/health
wget http://my-service:8080/data.json

# Docker build (pass proxy to build context)
docker build --build-arg http_proxy=http://host.docker.internal:3128 .

# npm / pip / other package managers
http_proxy=http://127.0.0.1:3128 npm install
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--listen` | `-l` | `127.0.0.1:3128` | Address to listen on |
| `--username` | `-u` | _(none)_ | Enable authentication with this username |
| `--password` | `-p` | _(none)_ | Password for authentication |
| `--cluster-domain` | | `cluster.local` | Kubernetes cluster domain for FQDN matching |

### Authentication

Enable HTTP Basic authentication (RFC 7617) to restrict access:

```bash
kubeport http-proxy --username admin --password secret
```

Clients include credentials in the proxy URL:

```bash
curl --proxy http://admin:secret@127.0.0.1:3128 http://my-service:8080/

# Or via environment variables
export http_proxy=http://admin:secret@127.0.0.1:3128
export https_proxy=http://admin:secret@127.0.0.1:3128
```

When authentication fails, the proxy returns `407 Proxy Authentication Required` with a `Proxy-Authenticate: Basic realm="kubeport"` header.

### How It Works

The proxy supports two modes depending on the request:

**Plain HTTP** — the proxy receives the full request, translates the `Host` header to the mapped localhost address, forwards the request, and returns the response. Proxy-specific headers (`Proxy-Authorization`, `Proxy-Connection`) are stripped before forwarding.

**HTTPS (CONNECT tunneling)** — the client sends a `CONNECT host:port` request. The proxy translates the address, establishes a TCP connection to the target, and then relays raw bytes in both directions. The TLS handshake happens directly between client and target.

Keep-alive connections are supported. If a subsequent request on the same connection targets a different host, the proxy returns `400 Bad Request` to prevent misrouting.

## Configuration File

Both proxy servers can be configured in `kubeport.yaml` (or `.toml`) instead of using flags. CLI flags take precedence over config file values.

### YAML

```yaml
context: my-cluster
namespace: default

services:
  - name: My API
    service: my-api
    local_port: 8080
    remote_port: 80

socks:
  enabled: true              # auto-start with daemon
  listen: 127.0.0.1:1080
  username: admin
  password: secret
  fuzzy_match: true          # default: true

http_proxy:
  enabled: true              # auto-start with daemon
  listen: 127.0.0.1:3128
  username: admin
  password: secret
  fuzzy_match: true          # default: true
```

### TOML

```toml
context = "my-cluster"
namespace = "default"

[socks]
enabled = true
listen = "127.0.0.1:1080"
username = "admin"
password = "secret"
fuzzy_match = true

[http_proxy]
enabled = true
listen = "127.0.0.1:3128"
username = "admin"
password = "secret"
fuzzy_match = true
```

### Config Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Auto-start with daemon (`kubeport start`/`fg`); when `false`, use `kubeport socks` or `kubeport http-proxy` to start manually |
| `listen` | string | `127.0.0.1:1080` (SOCKS) / `127.0.0.1:3128` (HTTP) | Listen address; overridden by `--listen` flag |
| `username` | string | _(none)_ | Authentication username |
| `password` | string | _(none)_ | Authentication password |
| `fuzzy_match` | bool | `true` | Enable headless service FQDN resolution (see below) |

> **Note:** The `--cluster-domain` flag is CLI-only and cannot be set in the config file. It defaults to `cluster.local`.

## Address Translation

Both proxies use kubeport's address mapping table (the same one shown by `kubeport mappings`) to translate Kubernetes addresses to localhost ports.

### Exact Match

If you have a forward for `redis-0:6379 -> 127.0.0.1:16379`, the proxy translates:

```
redis-0:6379  →  127.0.0.1:16379
```

### Kubernetes DNS Variants

The proxy recognizes standard Kubernetes DNS forms and matches them to the same forward:

```
redis-0:6379                                            →  127.0.0.1:16379
redis-0.redis.default:6379                              →  127.0.0.1:16379
redis-0.redis.default.svc.cluster.local:6379            →  127.0.0.1:16379
```

### Fuzzy Matching (Headless Services)

When `fuzzy_match` is enabled (the default), the proxy handles headless service FQDNs by extracting the pod name prefix:

```
redis-node-0.redis-headless.dev.svc.cluster.local:6379  →  redis-node-0:6379  →  127.0.0.1:16379
```

This is important for services like Redis Sentinel or MongoDB replica sets that return internal DNS names in their discovery responses.

Disable fuzzy matching (`fuzzy_match: false`) if you need strict exact-match-only resolution.

## Browser Configuration

### Firefox

1. Settings > Network Settings > Manual proxy configuration
2. SOCKS Host: `127.0.0.1`, Port: `1080`
3. Select SOCKS v5
4. Check "Proxy DNS when using SOCKS v5"

### Chrome (macOS)

```bash
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
  --proxy-server="socks5://127.0.0.1:1080"
```

### System-wide (macOS)

```bash
networksetup -setsocksfirewallproxy "Wi-Fi" 127.0.0.1 1080
# Remember to turn it off:
networksetup -setsocksfirewallproxystate "Wi-Fi" off
```

## Troubleshooting

### Proxy cannot connect to target

```
Error connecting to daemon: ...
Make sure kubeport is running (kubeport start)
```

The proxy needs a running kubeport daemon. Start the daemon first, then start the proxy.

### Address not resolved

If the proxy connects but returns a connection error, the target address may not be in kubeport's mapping table. Check:

```bash
kubeport mappings          # List all known address mappings
kubeport status            # Verify the forward is healthy
```

### Authentication failures

If you see `407 Proxy Authentication Required` (HTTP) or connection rejected (SOCKS), verify your credentials match the proxy configuration. Both username and password must be provided together.

### Port conflicts

If the default port is in use, specify a different listen address:

```bash
kubeport socks --listen 127.0.0.1:1081
kubeport http-proxy --listen 127.0.0.1:3129
```
