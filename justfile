# kubeport - Kubernetes port-forward supervisor

# Default recipe: show help
default:
    @just --list

# Build the binary with version info
build:
    go build -ldflags "-X github.com/rbaliyan/kubeport/internal/cli.Version=$(git describe --tags --always --dirty 2>/dev/null || echo dev) -X github.com/rbaliyan/kubeport/internal/cli.Commit=$(git rev-parse --short HEAD 2>/dev/null || echo unknown) -X github.com/rbaliyan/kubeport/internal/cli.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o bin/kubeport .

# Install to GOBIN
install:
    go install .

# Run tests
test:
    go test ./...

# Run tests with verbose output
test-v:
    go test -v ./...

# Run go vet
lint:
    go vet ./...

# Format code
fmt:
    gofmt -w .

# Tidy go modules
tidy:
    go mod tidy

# Clean build artifacts
clean:
    rm -rf bin/

# Build and run with example config
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
