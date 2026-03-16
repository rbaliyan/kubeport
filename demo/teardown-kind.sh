#!/usr/bin/env bash
#
# Tear down the demo kind cluster.
#
# Usage (from repo root or demo/):
#   ./demo/teardown-kind.sh

set -euo pipefail

CLUSTER_NAME="kind-cluster"

if ! command -v kind &>/dev/null; then
    echo "Error: kind is not installed"
    exit 1
fi

if ! kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
    echo "Cluster '${CLUSTER_NAME}' does not exist, nothing to do"
    exit 0
fi

echo "Deleting kind cluster '${CLUSTER_NAME}'..."
KIND_EXPERIMENTAL_PROVIDER=podman kind delete cluster --name "${CLUSTER_NAME}"

echo "Done."
