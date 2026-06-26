# kubeport - Kubernetes port-forward supervisor

# Default recipe: show help
default:
    @just --list

# Build the binary with version info
build:
    go build -ldflags "-s -w $(go-version ldflags -static)" -trimpath -o bin/kubeport .

# Install to GOBIN
install:
    go install -ldflags "-s -w $(go-version ldflags -static)" .

# Run tests
test:
    go test ./...

# Run tests with verbose output
test-v:
    go test -v ./...

# Run tests with race detector
test-race:
    go test -race ./...

# Run tests with coverage
test-cover:
    go test -cover ./...

# Run the benchmark suite (no unit tests; report allocations)
bench:
    go test -run='^$' -bench=. -benchmem ./...

# Run benchmarks and capture CPU/memory profiles for one package.
# Usage: just bench-profile PKG=./internal/proxy BENCH=BenchmarkManagerMappings
# Inspect with: go tool pprof cpu.prof  (or mem.prof)
# Compare two runs with benchstat (golang.org/x/perf/cmd/benchstat):
#   go test -run='^$' -bench=. -benchmem -count=10 ./... | tee new.txt
#   benchstat bench.txt new.txt
bench-profile PKG="./..." BENCH=".":
    go test -run='^$' -bench={{BENCH}} -benchmem -cpuprofile=cpu.prof -memprofile=mem.prof {{PKG}}

# Run the labeled smoke suite (fast, broad sanity checks)
smoke:
    go test -run '^TestSmoke' ./...

# Run a single fuzz target for a fixed duration.
# Usage: just fuzz FuzzUnmarshalYAML [TIME=30s]
fuzz TARGET TIME="30s":
    go test -run='^$' -fuzz='^{{TARGET}}$' -fuzztime={{TIME}} ./...

# Run every fuzz target briefly in sequence (smoke-level fuzzing).
# Targets are discovered by grepping for `func Fuzz` so new ones are picked up
# automatically. Override per-target duration with TIME (e.g. just fuzz-all 30s).
fuzz-all TIME="15s":
    #!/usr/bin/env bash
    set -euo pipefail
    targets=$(grep -rhoE '^func Fuzz[A-Za-z0-9_]*\(' --include='*_test.go' . | sed -E 's/^func (Fuzz[A-Za-z0-9_]*)\(/\1/' | sort -u)
    for t in $targets; do
        pkg=$(dirname "$(grep -rl "func ${t}(" --include='*_test.go' . | head -1)")
        echo "==> fuzzing ${t} in ${pkg} for {{TIME}}"
        go test -run='^$' -fuzz="^${t}\$" -fuzztime={{TIME}} "./${pkg}/"
    done

# Coverage gate: write a profile, strip generated/entrypoint/infra-bound lines,
# and print the filtered total. Exclusions:
#   - api/kubeport/v1: generated protobuf
#   - main.go: entrypoint (not unit-testable)
#   - internal/cli: infra-bound daemon-launch/dial command handlers
cover:
    go test -coverprofile=cover.out ./...
    @grep -vE 'api/kubeport/v1|/main\.go|internal/cli' cover.out > cover.filtered.out
    @go tool cover -func=cover.filtered.out | tail -1

# Format code
fmt:
    go fmt ./...

# Run golangci-lint (matches CI)
lint:
    golangci-lint run

# Run vulnerability check
vulncheck:
    go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# Check for outdated dependencies
depcheck:
    go list -m -u all | grep '\[' || echo "All dependencies are up to date"

# Tidy go modules
tidy:
    go mod tidy

# Clean build artifacts
clean:
    rm -rf bin/ dist/
    go clean -testcache

# Build and run with args
run *ARGS:
    go run . {{ARGS}}

# Generate protobuf code
proto:
    buf generate

# Lint protobuf definitions
proto-lint:
    buf lint

# Check everything (fmt + lint + test)
check: fmt lint test

# Build snapshot release (for testing)
snapshot:
    goreleaser release --snapshot --clean

# Check goreleaser config
check-release:
    goreleaser check

# Set up pre-commit hooks for local development
setup:
    pre-commit install
    @echo "Pre-commit hooks installed"

# Create and push a new release tag (bumps patch version)
release:
    ./scripts/release.sh
