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

# Format code
fmt:
    go fmt ./...

# Run go vet
lint:
    go vet ./...

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

# Create and push a new release tag (bumps patch version)
release:
    ./scripts/release.sh
