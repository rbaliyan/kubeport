# Installation

## Homebrew (macOS / Linux)

The easiest way to install kubeport:

```bash
brew install rbaliyan/tap/kubeport
```

Upgrades:

```bash
brew upgrade kubeport
```

## Install Script

A one-liner that detects your OS and architecture automatically:

```bash
curl -sSfL https://raw.githubusercontent.com/rbaliyan/kubeport/main/install.sh | sh
```

Install to a custom directory:

```bash
curl -sSfL https://raw.githubusercontent.com/rbaliyan/kubeport/main/install.sh | sh -s -- -b /usr/local/bin
```

Install a specific version:

```bash
curl -sSfL https://raw.githubusercontent.com/rbaliyan/kubeport/main/install.sh | sh -s -- v1.2.0
```

The script installs to `~/.local/bin` by default. Make sure this directory is in your `PATH`.

## Go Install

If you have Go installed:

```bash
go install github.com/rbaliyan/kubeport@latest
```

## Download Binary

Pre-built binaries for Linux and macOS (amd64/arm64) are available on the [GitHub Releases](https://github.com/rbaliyan/kubeport/releases) page.

Release archives include the binary, example configs, and shell completions.

## Package Managers

Release binaries are also packaged as `.deb`, `.rpm`, and `.apk` for Linux distributions. Download them from the [releases page](https://github.com/rbaliyan/kubeport/releases).

## Verify Installation

```bash
kubeport version
```

## Requirements

- Access to a Kubernetes cluster (via `~/.kube/config` or `KUBECONFIG`)
- `kubectl` is **not** required — kubeport uses [client-go](https://github.com/kubernetes/client-go) directly

## Shell Completions

Shell completions are included in release archives. See the [Shell Completions](shell-completions.md) guide for setup instructions.
