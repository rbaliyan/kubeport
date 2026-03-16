FROM golang:1.25-alpine AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_TIMESTAMP=unknown

RUN CGO_ENABLED=0 go build \
    -ldflags "-s -w \
      -X github.com/rbaliyan/go-version.VersionInfo=${VERSION} \
      -X github.com/rbaliyan/go-version.GitCommit=${GIT_COMMIT} \
      -X github.com/rbaliyan/go-version.BuildTimestamp=${BUILD_TIMESTAMP} \
      -X github.com/rbaliyan/kubeport/internal/cli.InstallMethod=docker" \
    -trimpath \
    -o kubeport .

# Create the data directory here so we can COPY it with the right ownership below.
RUN mkdir -p /data

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /build/kubeport /usr/local/bin/kubeport

# /data is owned by nonroot (65532) — no host-side chown needed when mounting a volume.
# Mount your kubeport.yaml here. PID and log files are written alongside the config.
# When running inside a cluster, a service account with port-forward permissions is used automatically.
COPY --from=builder --chown=65532:65532 /data /data
VOLUME ["/data"]

# gRPC API port — set listen: tcp://0.0.0.0:19191 and api_key in your config.
EXPOSE 19191

# Runs as uid 65532 (nonroot) — inherited from the base image.
USER nonroot:nonroot

ENTRYPOINT ["kubeport", "foreground"]
CMD ["--config", "/data/kubeport.yaml"]
