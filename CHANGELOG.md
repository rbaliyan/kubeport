# Changelog

All notable changes to kubeport are documented here. This project uses [semantic versioning](https://semver.org/).

## [Unreleased]

### Added
- Fuzz tests and ClusterFuzzLite CI workflows
- Build provenance attestation to release workflow
- `pkg/proxy.GRPCDialOption()` method for per-client gRPC resolver registration
- `pkg/proxy.WithRefreshInterval()` option to control auto-refresh interval
- `kubeport instances` command lists all running daemon instances with PID, uptime, version, endpoint, API-key hint, and paths
- `--offload` flag on `start` redirects services to an already-running instance instead of launching a new daemon
- Central instance registry at `~/.config/kubeport/instances.json` tracks all running kubeport daemons

### Changed
- Daemon server RPCs now return proper gRPC status codes instead of response `Error` fields
- Promoted `internal/config` to `pkg/config` — config parsing is now a public API
- Extracted `pkg/grpcauth` for reusable gRPC auth interceptors
- Hook event names use colon-based namespacing (e.g., `forward:connected`); legacy underscore names auto-migrate
- Config files written with `0600` permissions (was `0644`)
- Hook concrete types (`ShellHook`, `WebhookHook`, `ExecHook`) are now unexported
- Runtime files (PID, socket, log) now live in `~/.config/kubeport/` per-instance instead of next to the config file
- Stale `.kubeport.pid` and `.kubeport.sock` files from older versions are auto-removed on daemon start

### Fixed
- Race condition in `pkg/proxy` address translation (mutex snapshot)
- Ticker leak in proxy refresh goroutine
- Global gRPC resolver registration replaced with per-client `grpc.WithResolvers()`

## [v0.6.1] - 2026-03-05

### Added
- Fuzz tests and ClusterFuzzLite CI integration
- Build provenance attestation for release artifacts

### Changed
- Rewrite README with accurate feature coverage

## [v0.6.0] - 2026-02-27

### Added
- Multi-port service forward with auto-discovery (`ports: all` and named port lists)
- TCP listen with API key authentication for remote daemon control
- Dependabot grouping to combine dependency PRs

### Fixed
- Prefer ready pods and skip terminating pods on reconnect
- Bounds check for localPortOffset before int32 conversion

### Changed
- Restructured README and added detailed documentation

## [v0.5.1] - 2026-02-20

### Added
- `log_file` and `listen` config fields

### Fixed
- Preserve status ordering and clear stale errors on reconnect
- Bump Go toolchain to 1.25.7 to fix crypto/tls vulnerability

## [v0.5.0] - 2026-02-20

### Added
- Daemon management RPCs (reload, apply, restart)
- Version reporting in daemon status
- Status enhancements

### Changed
- Bumped google.golang.org/grpc to 1.79.1
- Bumped k8s.io/client-go to 0.35.1

## [v0.4.3] - 2026-02-10

### Fixed
- Switch from Homebrew cask to formula to avoid Gatekeeper warnings

## [v0.4.2] - 2026-02-10

### Added
- Homebrew cask distribution via rbaliyan/homebrew-tap

## [v0.4.1] - 2026-02-10

### Changed
- Harden APIs, fix data races, and deduplicate validation
- Improve OpenSSF Scorecard (security policy, token permissions, pinned deps)

## [v0.4.0] - 2026-02-07

### Changed
- Update workflow Go versions, remove Windows builds, add badges
- Code quality improvements and lint fixes
- Use golangci-lint v2 for Go 1.25

## [v0.3.0] - 2026-02-06

### Added
- Named port resolution
- CLI enhancements
- go-version integration for embedded build metadata
- JSON status output

## [v0.2.0] - 2026-02-06

Initial public release with core features:
- Port-forward supervision with health checks and auto-restart
- YAML/TOML configuration
- Background daemon with gRPC control
- Lifecycle hooks (shell, exec, webhook)
- Shell completions (bash, zsh, fish)

[Unreleased]: https://github.com/rbaliyan/kubeport/compare/v0.6.1...HEAD
[v0.6.1]: https://github.com/rbaliyan/kubeport/compare/v0.6.0...v0.6.1
[v0.6.0]: https://github.com/rbaliyan/kubeport/compare/v0.5.1...v0.6.0
[v0.5.1]: https://github.com/rbaliyan/kubeport/compare/v0.5.0...v0.5.1
[v0.5.0]: https://github.com/rbaliyan/kubeport/compare/v0.4.3...v0.5.0
[v0.4.3]: https://github.com/rbaliyan/kubeport/compare/v0.4.2...v0.4.3
[v0.4.2]: https://github.com/rbaliyan/kubeport/compare/v0.4.1...v0.4.2
[v0.4.1]: https://github.com/rbaliyan/kubeport/compare/v0.4.0...v0.4.1
[v0.4.0]: https://github.com/rbaliyan/kubeport/compare/v0.3.0...v0.4.0
[v0.3.0]: https://github.com/rbaliyan/kubeport/compare/v0.2.0...v0.3.0
[v0.2.0]: https://github.com/rbaliyan/kubeport/releases/tag/v0.2.0
