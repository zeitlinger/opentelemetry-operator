#!/bin/bash
# Build the injector from source and run the mode e2e tests.
#
# Prerequisites:
#   - docker
#   - kind cluster running (make prepare-e2e)
#   - operator deployed (make deploy)
#
# Usage:
#   ./hack/run-injector-mode-e2e.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
OPERATOR_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
INJECTOR_DIR="${INJECTOR_DIR:-$HOME/source/opentelemetry-injector}"
KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-otel-operator-e2e}"

echo "=== Step 1: Build injector image from source ==="
echo "Building from: $INJECTOR_DIR"
docker build \
  -f "$INJECTOR_DIR/Dockerfile.operator-e2e" \
  -t local/injector:dev \
  "$INJECTOR_DIR"

echo ""
echo "=== Step 2: Build Java agent image ==="
docker build \
  --platform linux/amd64 \
  -t local/injector-java:dev \
  "$OPERATOR_DIR/images/injector-java"

echo ""
echo "=== Step 3: Load images into kind ==="
kind load docker-image local/injector:dev --name "$KIND_CLUSTER_NAME"
kind load docker-image local/injector-java:dev --name "$KIND_CLUSTER_NAME"

echo ""
echo "=== Step 4: Run mode e2e tests ==="
cd "$OPERATOR_DIR"
chainsaw test \
  --test-dir tests/e2e-instrumentation/injector-mode-conflict \
  --test-dir tests/e2e-instrumentation/injector-mode-force

echo ""
echo "=== All mode e2e tests passed ==="
