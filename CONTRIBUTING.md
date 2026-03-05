# Contributing to kubeport

Contributions are welcome! This guide explains how to set up a development environment, run tests, and submit changes.

## Prerequisites

- [Go 1.25+](https://go.dev/dl/)
- [mise](https://mise.jdx.dev) (recommended) or manually install the tools listed in `.mise.toml`
- [just](https://github.com/casey/just) command runner
- Access to a Kubernetes cluster (for manual testing)

## Development Setup

```bash
# Clone the repository
git clone https://github.com/rbaliyan/kubeport.git
cd kubeport

# Install tool versions via mise
mise install

# Build the binary
just build

# Run tests
just test

# Run the linter
just lint

# Format code
just fmt
```

All available recipes are listed in the [justfile](justfile). Run `just` with no arguments to see them.

## Running Tests

```bash
just test          # Standard test run
just test-v        # Verbose output
just test-race     # With race detector (CI uses this)
just test-cover    # With coverage

# Run a single test
go test -run TestName ./internal/...
```

## Code Style

- `ctx context.Context` as the first parameter
- Return value types, not pointers to value types (e.g., `(Value, error)` not `(*Value, error)`)
- Unexport internal types; only expose Option functions and interfaces
- No panics except in initialization
- Functional options pattern: `New(name, ...Option) (*T, error)`

## Protobuf

If you modify `.proto` files, regenerate the Go code:

```bash
just proto
```

This requires `buf`, `protoc-gen-go`, and `protoc-gen-go-grpc` (all managed by mise).

## Submitting Changes

1. Fork the repository
2. Create a feature branch from `main` (`git checkout -b my-feature`)
3. Make your changes
4. Run `just check` (formats, lints, and tests in one step)
5. Commit using [conventional commit](https://www.conventionalcommits.org/) format:
   - `feat: add new feature`
   - `fix: resolve issue with ...`
   - `docs: update configuration guide`
   - `refactor: simplify proxy manager`
   - `ci: update GitHub Actions workflow`
6. Push your branch and open a pull request against `main`

## Pull Request Guidelines

- Keep PRs focused on a single change
- Include tests for new functionality
- Update documentation if behavior changes
- CI must pass (tests with race detector + linting)

## Reporting Issues

Use [GitHub Issues](https://github.com/rbaliyan/kubeport/issues) for bug reports and feature requests. For security vulnerabilities, see [SECURITY.md](SECURITY.md).

## License

By contributing, you agree that your contributions will be licensed under the [MIT License](LICENSE).
