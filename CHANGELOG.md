# Changelog

All notable changes to kubeport are documented here. This project uses [semantic versioning](https://semver.org/).

## [v0.8.1] - 2026-05-14

### Added
- `--verbose` flag and `log_level` config field — set daemon log verbosity to `debug`, `info` (default), `warn`, or `error`; `--verbose` forces `debug` regardless of the configured level.

### Fixed
- `kubeport start --wait` now treats `external` forwards as ready instead of timing out — when a service is owned by another running instance, `--wait` no longer blocks waiting for a local forward that will never start.

## [v0.8.0] - 2026-05-04

### Added
- `--delegate` flag on `kubeport start` — starts a lease-holder daemon that hands off all its services to an existing primary daemon via gRPC `AddService` (with `source_config` set to the delegate's config path), runs no local port-forwards itself, and on shutdown calls `ReleaseBySource` on the primary to bulk-remove only the services it contributed. Falls back to a regular start if no primary is running. Delegate instances are tracked in the registry with `Delegate: true` and `PrimarySocket` pointing at the primary, and shown as `ROLE: delegate` in `kubeport instances`.
- `ReleaseBySource` gRPC RPC — removes all forwards contributed by a given `source_config` path. Used by delegate instances for clean teardown.
- Auto external-conflict detection on `kubeport start` (no flag) — at startup and on every reload (SIGHUP / file change) the daemon scans the instance registry; services already owned by another non-delegate instance (matched by service name OR static `local_port`) are marked `external` and not started locally. They appear in `kubeport status` / `kubeport watch` as `⤵ external [managed by PID X]` and are reclaimed automatically when the owning instance stops. Delegates are excluded from the scan (only primaries count).
- `FORWARD_STATE_EXTERNAL = 6` proto enum and `external_instance` (field 26) / `external_pid` (field 27) on `ForwardStatusProto`.
- `ROLE` column in `kubeport instances` table (`primary` white / `delegate` yellow); delegate detail block shows `Primary: <socket>`.
- Pre-flight check before auto-starting SOCKS or HTTP proxy: if the configured listen address is already bound, kubeport now fails with a clear "Only one kubeport instance may run a proxy on the same address" error instead of failing silently.
- `connection_mode` service and supervisor field (`"mux"` default, `"isolated"`): in `isolated` mode each incoming TCP connection gets its own dedicated SPDY tunnel to the API server, removing the ~128-concurrent-client limit imposed by the SPDY 256-stream cap. Can be set per-service or globally via `supervisor.connection_mode`; per-service setting takes precedence. Not supported with `ports: all` / multi-port mode.
- `kubeport chaos` command for live chaos mutation without reload: `set`, `enable`, `disable`, `preset`, and `reset` subcommands
- Built-in chaos presets: `slow-network` (200ms latency spikes, 10% probability), `unstable-cluster` (5% errors + 5% 2s spikes), `packet-loss` (15% connection errors)
- `UpdateChaos` gRPC RPC — chaos overrides take effect on active tunnels via atomic pointer swap; no reconnect needed
- Fuzz tests and ClusterFuzzLite CI workflows
- Build provenance attestation to release workflow
- `pkg/proxy.GRPCDialOption()` method for per-client gRPC resolver registration
- `pkg/proxy.WithRefreshInterval()` option to control auto-refresh interval
- `kubeport instances` command lists all running daemon instances with PID, uptime, version, endpoint, API-key hint, and paths
- `--offload` flag on `start` redirects services to an already-running instance instead of launching a new daemon
- Central instance registry at `~/.config/kubeport/instances.json` tracks all running kubeport daemons
- `extends` field in config files for config inheritance — a project config can inherit `api_key`, `context`, `supervisor`, and other settings from a parent (e.g., `~/.config/kubeport/global.yaml`); chains and circular-reference detection are supported
- `key_id` config field — key identifier sent alongside `api_key` for token rotation
- `lazy` service field — defers opening the SPDY tunnel until the first client connection, reducing idle resource use

### Changed
- Daemon server RPCs now return proper gRPC status codes instead of response `Error` fields
- Promoted `internal/config` to `pkg/config` — config parsing is now a public API
- Extracted `pkg/grpcauth` for reusable gRPC auth interceptors
- Hook event names use colon-based namespacing (e.g., `forward:connected`); legacy underscore names auto-migrate
- Config files written with `0600` permissions (was `0644`)
- Hook concrete types (`ShellHook`, `WebhookHook`, `ExecHook`) are now unexported
- Runtime files (PID, socket, log) now live in `~/.config/kubeport/` per-instance instead of next to the config file
- Stale `.kubeport.pid` and `.kubeport.sock` files from older versions are auto-removed on daemon start
- `chaos.enabled`, `socks.enabled`, and `http_proxy.enabled` are now pointer booleans (`*bool`): omitting the field (nil) means "inherit from parent config", `false` explicitly disables even if the parent config enables it

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

[Unreleased]: https://github.com/rbaliyan/kubeport/compare/v0.8.1...HEAD
[v0.8.1]: https://github.com/rbaliyan/kubeport/compare/v0.8.0...v0.8.1
[v0.8.0]: https://github.com/rbaliyan/kubeport/compare/v0.7.3...v0.8.0
[v0.7.3]: https://github.com/rbaliyan/kubeport/compare/v0.7.2...v0.7.3
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
