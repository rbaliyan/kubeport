#!/usr/bin/env bash
#
# Set up a kind cluster (using Podman) and deploy demo services.
#
# Usage (from repo root or demo/):
#   ./demo/setup-kind.sh
#
# What this does:
#   1. Creates a kind cluster named "kind-cluster" using Podman
#   2. Deploys all demo services (web-api, cache, platform) into the "demo" namespace
#   3. Waits for all deployments to be ready

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# ── Prerequisites ──────────────────────────────────────────────────────────────

for cmd in kind kubectl podman; do
    if ! command -v "$cmd" &>/dev/null; then
        echo "Error: $cmd is not installed"
        exit 1
    fi
done

if ! podman machine list 2>/dev/null | grep -q "Currently running"; then
    echo "Error: Podman machine is not running. Start it with: podman machine start"
    exit 1
fi

# ── Cluster ────────────────────────────────────────────────────────────────────

CLUSTER_NAME="kind-cluster"
CONTEXT="kind-${CLUSTER_NAME}"

if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
    echo "Cluster '${CLUSTER_NAME}' already exists, skipping creation"
else
    echo "Creating kind cluster '${CLUSTER_NAME}' using Podman..."
    KIND_EXPERIMENTAL_PROVIDER=podman kind create cluster --name "${CLUSTER_NAME}"
fi

echo ""
echo "Context: ${CONTEXT}"

# ── Demo services ──────────────────────────────────────────────────────────────

echo ""
echo "Deploying demo services..."
kubectl --context "${CONTEXT}" apply -f "${SCRIPT_DIR}/k8s-setup.yaml"

echo ""
echo "Waiting for deployments to be ready..."
kubectl --context "${CONTEXT}" -n demo wait \
    --for=condition=available deployment --all --timeout=120s

echo ""
echo "Done. Demo cluster is ready."
echo ""
echo "Next steps:"
echo "  just build              # build the kubeport binary"
echo "  ./demo/demo.sh          # record the demo GIF"
