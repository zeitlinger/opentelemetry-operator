# Image Volumes — Local Testing (v1alpha1)

This documents local testing of image volume injection for the **v1alpha1 per-language instrumentation** API
(`internal/instrumentation/`).

Image volumes replace the init container + emptyDir approach for agent injection. They require:

- **Kubernetes 1.31+** — when the `ImageVolume` feature gate was introduced (alpha)
- **containerd 2.1+** — required by the kubelet to actually mount image volumes
- **`ImageVolume` feature gate** — must be enabled on the API server and kubelet

The operator detects Kubernetes >= 1.31 and automatically uses image volumes when available, falling back to init containers on older clusters.

> **Note for kind:** `kindest/node:v1.31.x` ships with containerd 1.7.x which predates image volume support. Use `kindest/node:v1.32.x` or later, which bundles containerd 2.1.

## Create the kind cluster

Uses `kindest/node:v1.32.5` since that is the earliest kind node image that bundles containerd 2.1.

```bash
cat <<EOF | kind create cluster --name otel-operator-dev --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  image: kindest/node:v1.32.5
  kubeadmConfigPatches:
  - |
    kind: ClusterConfiguration
    apiServer:
      extraArgs:
        feature-gates: "ImageVolume=true"
  - |
    kind: KubeletConfiguration
    featureGates:
      ImageVolume: true
EOF
```

## Install cert-manager

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.19.4/cert-manager.yaml
kubectl wait --for=condition=Available deployments/cert-manager -n cert-manager --timeout=120s
```

## Build and deploy the operator

```bash
IMG=opentelemetry-operator:dev make container
kind load docker-image opentelemetry-operator:dev --name otel-operator-dev
IMG=opentelemetry-operator:dev make deploy
kubectl rollout status deployment/opentelemetry-operator-controller-manager \
  -n opentelemetry-operator-system --timeout=120s
```

## Deploy the OpenTelemetry Collector

```bash
kubectl apply -f - <<EOF
apiVersion: opentelemetry.io/v1beta1
kind: OpenTelemetryCollector
metadata:
  name: otelcol
spec:
  config:
    receivers:
      otlp:
        protocols:
          grpc:
            endpoint: 0.0.0.0:4317
          http:
            endpoint: 0.0.0.0:4318
    exporters:
      debug:
        verbosity: detailed
    service:
      pipelines:
        traces:
          receivers: [otlp]
          exporters: [debug]
        metrics:
          receivers: [otlp]
          exporters: [debug]
        logs:
          receivers: [otlp]
          exporters: [debug]
EOF
kubectl wait --for=condition=Available deployment/otelcol-collector --timeout=120s
```

## Create the Instrumentation CR

```bash
kubectl apply -f - <<EOF
apiVersion: opentelemetry.io/v1alpha1
kind: Instrumentation
metadata:
  name: test-instrumentation
spec:
  python:
    image: ghcr.io/open-telemetry/opentelemetry-operator/autoinstrumentation-python:latest
  exporter:
    endpoint: http://otelcol-collector:4318
EOF
```

## Test Python

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: test-python-app
  annotations:
    instrumentation.opentelemetry.io/inject-python: "true"
spec:
  containers:
  - name: app
    image: python:3.11-slim
    command: ["sleep", "infinity"]
EOF
kubectl wait --for=condition=Ready pod/test-python-app --timeout=120s
```

Verify the image volume was injected (no init container, volume type is `image`):

```bash
kubectl get pod test-python-app -o json | python3 -c "
import json, sys
pod = json.load(sys.stdin)
print('=== Volumes ===')
for v in pod['spec']['volumes']:
    print(' ', v['name'], '->', list(v.keys()))
print()
print('=== Init containers ===')
for c in pod['spec'].get('initContainers', []):
    print(' ', c['name'])
print('  (none)' if not pod['spec'].get('initContainers') else '')
print()
print('=== Volume mounts ===')
for m in pod['spec']['containers'][0]['volumeMounts']:
    print(' ', m['mountPath'], '<-', m['name'])
"
```

Expected output:
```
=== Volumes ===
  kube-api-access-xxxxx -> ['name', 'projected']
  opentelemetry-auto-instrumentation-python -> ['image', 'name']

=== Init containers ===
  (none)

=== Volume mounts ===
  /var/run/secrets/kubernetes.io/serviceaccount <- kube-api-access-xxxxx
  /otel-auto-instrumentation-python <- opentelemetry-auto-instrumentation-python
```

Verify the agent is reachable and `PYTHONPATH` points into the `/autoinstrumentation` subdirectory
(image volumes mount the full image filesystem, so agent files are one level deeper than with init containers):

```bash
# sitecustomize.py bootstraps the SDK — if this prints a path, injection is working
kubectl exec test-python-app -- python3 -c "import sitecustomize; print(sitecustomize.__file__)"

# PYTHONPATH should include /autoinstrumentation/ in the paths
kubectl exec test-python-app -- env | grep PYTHONPATH
```

Expected:
```
/otel-auto-instrumentation-python/autoinstrumentation/opentelemetry/instrumentation/auto_instrumentation/sitecustomize.py
PYTHONPATH=/otel-auto-instrumentation-python/autoinstrumentation/opentelemetry/instrumentation/auto_instrumentation:/otel-auto-instrumentation-python/autoinstrumentation
```

Send a trace and verify it appears in the collector logs:

```bash
# Watch collector in one terminal
kubectl logs deployment/otelcol-collector -f

# Trigger HTTP activity in another terminal
kubectl exec test-python-app -- python3 -c "
import urllib.request
urllib.request.urlopen('http://example.com')
print('done')
"
```

You should see a trace with spans for the outgoing HTTP request appear in the collector logs.

## Cleanup

```bash
kind delete cluster --name otel-operator-dev
```
