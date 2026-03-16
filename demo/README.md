# Demo

This directory contains everything needed to record the kubeport demo GIF.

## Prerequisites

Install the required tools:

```bash
brew install kind kubectl asciinema agg
brew install podman
```

Start the Podman machine if it is not already running:

```bash
podman machine start
```

## Set up the kind cluster

Create the cluster and deploy the demo services:

```bash
./demo/setup-kind.sh
```

This creates a kind cluster named `kind-cluster` using Podman as the container
runtime and deploys three services into the `demo` namespace:

| Service    | Ports          | Description                        |
|------------|----------------|------------------------------------|
| `web-api`  | 80 (http)      | Nginx — simulates a web service    |
| `cache`    | 6379 (redis)   | Redis — simulates a cache          |
| `platform` | 80 (http), 9113 (metrics) | Nginx — simulates a multi-port service |

## Build the binary

```bash
just build
```

## Record the demo

```bash
./demo/demo.sh
```

Output: `demo/demo.gif`

To write the GIF to a different path:

```bash
./demo/demo.sh --output /path/to/output.gif
```

## Tear down

```bash
./demo/teardown-kind.sh
```

## Demo config

`kubeport.yaml` is the config used during the recording. It targets the
`kind-kind-cluster` context and demonstrates:

- Single-port forward with an explicit local port (`web-api` → `localhost:8080`)
- Single-port forward with a dynamic OS-assigned local port (`cache`)
- Multi-port forward with dynamic local ports (`platform`, ports: all)
- SOCKS5 proxy pre-configured on `127.0.0.1:1080`
